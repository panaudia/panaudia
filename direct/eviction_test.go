package direct

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/sessions"
)

// admitOwned admits a node whose FuncSession simulates a real transport:
// Kill triggers the "owner goroutine" running the departure (Stop).
func admitOwned(t *testing.T, backend *DirectBackend, id uuid.UUID, name string) (*ConnectionHandler, *atomic.Int32) {
	t.Helper()
	var kills atomic.Int32
	var handler *ConnectionHandler
	live := &sessions.FuncSession{
		KillFn: func(string) {
			kills.Add(1)
			go handler.Stop()
		},
	}
	h, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, name), nil, live, "test")
	if serr != nil || h == nil {
		t.Fatalf("admission of %s failed: %v", name, serr)
	}
	handler = h.(*ConnectionHandler)
	return handler, &kills
}

// TestEvictionOnLiveDuplicate: a new connection for a uuid with a LIVE
// session evicts it — Kill, full announced departure, then the new
// session is admitted and works.
func TestEvictionOnLiveDuplicate(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	id := uuid.New()
	h1, kills := admitOwned(t, backend, id, "first")
	firstEntry := h1.registryEntry
	sendOp(t, backend, "attributes", id.String()+".name", "first", id)
	waitForCacheSet(t, backend, "attributes", id.String()+".name")

	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)
	backend.Sessions.Register(observerID, &sessions.FuncSession{}, "observer")

	// Reconnect while the first session is fully live.
	h2, kills2 := admitOwned(t, backend, id, "second")
	defer h2.Stop()

	if kills.Load() != 1 {
		t.Errorf("old session killed %d times, want 1", kills.Load())
	}
	if kills2.Load() != 0 {
		t.Error("new session was killed during its own admission")
	}
	select {
	case <-firstEntry.Departed():
	default:
		t.Error("old entry not departed after eviction")
	}
	if got := backend.Sessions.Get(id); got == nil || got == firstEntry || got.State() != sessions.Live {
		t.Fatalf("registry does not hold the new live session: %v", got)
	}
	if backend.Evictions.Load() != 1 {
		t.Errorf("eviction counter = %d, want 1", backend.Evictions.Load())
	}

	// The eviction produced exactly one full sweep…
	if !waitFor(time.Second, func() bool {
		_, _, g := sweepCounts(observer)
		return g == 1
	}) {
		t.Error("eviction did not announce the old session's departure")
	}
	// …and the survivor's fresh writes land unshadowed.
	sendOp(t, backend, "attributes", id.String()+".name", "second", id)
	waitForCacheSet(t, backend, "attributes", id.String()+".name")
}

// TestEvictionTimeoutFallback: the old session's owner is hung (Kill
// goes nowhere) — admission departs it directly after the bounded wait
// and still succeeds.
func TestEvictionTimeoutFallback(t *testing.T) {
	oldTimeout := admissionWaitTimeout
	admissionWaitTimeout = 100 * time.Millisecond
	defer func() { admissionWaitTimeout = oldTimeout }()

	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	id := uuid.New()
	// No KillFn effect: simulates a wedged owner that never departs.
	h1, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "hung"), nil, &sessions.FuncSession{}, "test")
	if serr != nil {
		t.Fatalf("admission failed: %v", serr)
	}
	firstEntry := h1.(*ConnectionHandler).registryEntry

	start := time.Now()
	h2, serr2 := backend.NewConnectionHandlerWithError(noInputConfig(id, "second"), nil, &sessions.FuncSession{}, "test")
	if serr2 != nil || h2 == nil {
		t.Fatalf("eviction fallback did not admit the new session: %v", serr2)
	}
	defer h2.Stop()
	if elapsed := time.Since(start); elapsed < admissionWaitTimeout {
		t.Errorf("fallback fired before the bounded wait (%v)", elapsed)
	}
	select {
	case <-firstEntry.Departed():
	default:
		t.Error("hung session not departed by the fallback")
	}
}

// TestReconnectStorm: the same identity connects 20× rapidly — exactly
// one live session at quiesce, 19 evictions, the survivor's keys fresh
// and unshadowed, and no goroutine/map debris.
func TestReconnectStorm(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	id := uuid.New()
	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)
	backend.Sessions.Register(observerID, &sessions.FuncSession{}, "observer")

	var last *ConnectionHandler
	for i := 0; i < 20; i++ {
		h, _ := admitOwned(t, backend, id, "storm")
		sendOp(t, backend, "attributes", id.String()+".name", "gen", id)
		last = h
	}

	if got := backend.Evictions.Load(); got != 19 {
		t.Errorf("evictions = %d, want 19", got)
	}
	// Exactly one live session, owned by the last admission.
	e := backend.Sessions.Get(id)
	if e == nil || e.State() != sessions.Live || e != last.registryEntry {
		t.Fatalf("survivor mismatch: %v", e)
	}
	backend.Lock()
	if backend.HandlersByUuid[id] != last {
		t.Error("handler map does not hold the survivor")
	}
	backend.Unlock()

	// Survivor's state is fresh (set after its admission) and stays
	// unshadowed by the 19 departures' tombstones.
	sendOp(t, backend, "attributes", id.String()+".name", "final", id)
	waitForCacheSet(t, backend, "attributes", id.String()+".name")
	time.Sleep(100 * time.Millisecond) // let any straggling sweeps land
	present, tomb := cacheState(backend, "attributes", id.String()+".name")
	if !present || tomb {
		t.Errorf("survivor's keys shadowed (present=%v tombstoned=%v)", present, tomb)
	}
	// 19 departures announced.
	if !waitFor(2*time.Second, func() bool {
		_, _, g := sweepCounts(observer)
		return g >= 19
	}) {
		_, _, g := sweepCounts(observer)
		t.Errorf("observer saw %d Gone datagrams, want 19", g)
	}

	last.Stop()
}

// TestShutdownDrainsAll: Shutdown kills every live session, each gets
// its full announced departure, and the registry/maps end empty.
func TestShutdownDrainsAll(t *testing.T) {
	backend := newTestBackend()
	backend.ISpace = newFakeSpace()

	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)
	backend.Sessions.Register(observerID, &sessions.FuncSession{}, "observer")

	const n = 5
	for i := 0; i < n; i++ {
		id := uuid.New()
		admitOwned(t, backend, id, "node")
		sendOp(t, backend, "entity", id.String(), true, id)
	}

	backend.Shutdown(2 * time.Second)

	// All sessions departed (observer remains registered — its stub
	// session has no transport to kill but Shutdown force-completes it).
	for _, e := range backend.Sessions.Snapshot() {
		t.Errorf("entry still registered after shutdown: %s (%s)", e.Uuid, e.Transport)
	}
	backend.Lock()
	handlers, bouncers := len(backend.HandlersByUuid), len(backend.BouncersByUuid)
	backend.Unlock()
	if handlers != 0 {
		t.Errorf("%d handlers left after shutdown", handlers)
	}
	if bouncers != 0 {
		t.Errorf("%d bouncers left after shutdown", bouncers)
	}
	// The n node departures were announced (the observer's own bouncer
	// was stopped by its departure at some point during the drain, so
	// it may have missed sweeps ordered after its own — count what
	// arrived; at least the sweeps before its departure must be there).
	_, _, g := sweepCounts(observer)
	if g == 0 {
		t.Error("no departure announcements observed during shutdown")
	}
	// Dispatcher stopped: Stop() already ran inside Shutdown; a second
	// Stop must be a no-op (idempotent).
	backend.Stop()
}

// TestShutdownForceCompletesOwnerless: sessions whose Kill goes nowhere
// are force-departed within the bound.
func TestShutdownForceCompletesOwnerless(t *testing.T) {
	backend := newTestBackend()
	backend.ISpace = newFakeSpace()

	ids := make([]uuid.UUID, 3)
	for i := range ids {
		ids[i] = uuid.New()
		_, serr := backend.NewConnectionHandlerWithError(noInputConfig(ids[i], "x"), nil, &sessions.FuncSession{}, "test")
		if serr != nil {
			t.Fatalf("admission failed: %v", serr)
		}
	}

	start := time.Now()
	backend.Shutdown(300 * time.Millisecond)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("shutdown took %v — not bounded", elapsed)
	}
	if left := len(backend.Sessions.Snapshot()); left != 0 {
		t.Errorf("%d sessions left after shutdown", left)
	}
}
