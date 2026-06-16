package direct

import (
	"math/rand"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
)

// admitWithAlive admits a node whose Alive() is driven by the returned
// flag (true = alive).
func admitWithAlive(t *testing.T, backend *DirectBackend, id uuid.UUID) (*ConnectionHandler, *atomic.Bool) {
	t.Helper()
	alive := &atomic.Bool{}
	alive.Store(true)
	live := &sessions.FuncSession{
		AliveFn: func() bool { return alive.Load() },
	}
	h, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "node"), nil, live, "test")
	if serr != nil || h == nil {
		t.Fatalf("admission failed: %v", serr)
	}
	return h.(*ConnectionHandler), alive
}

// TestReconcilerSweepsDeadSession: a session whose transport reports
// dead is departed by the pass with the full announcement — the ROC
// primary path and the missed-event backstop.
func TestReconcilerSweepsDeadSession(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	id := uuid.New()
	_, alive := admitWithAlive(t, backend, id)
	sendOp(t, backend, "attributes", id.String()+".name", "n", id)
	sendOp(t, backend, "entity", id.String(), true, id)
	waitForCacheSet(t, backend, "entity", id.String())

	// The observer needs a registry entry of its own, or the orphan
	// walk sweeps it (and a stopped bouncer drops deliveries).
	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)
	backend.Sessions.Register(observerID, &sessions.FuncSession{}, "observer")
	firstSeen := make(map[uuid.UUID]time.Time)

	// Healthy: untouched.
	backend.reconcilePass(0, firstSeen)
	if backend.Sessions.Get(id) == nil {
		t.Fatal("reconciler departed a healthy session")
	}

	// Transport dies without any event reaching the backend.
	alive.Store(false)
	backend.reconcilePass(0, firstSeen)

	if backend.Sessions.Get(id) != nil {
		t.Fatal("reconciler did not depart the dead session")
	}
	// Wire output identical to an event-driven departure.
	if !waitFor(time.Second, func() bool {
		a, e, g := sweepCounts(observer)
		return a == 1 && e == 1 && g == 1
	}) {
		a, e, g := sweepCounts(observer)
		t.Errorf("sweep counts = attrs:%d entity:%d gone:%d, want 1/1/1", a, e, g)
	}
	waitForTombstone(t, backend, "entity", id.String())
}

// TestReconcilerMinAgeGuard: a dead-looking session younger than the
// minimum age is untouched (mid-handshake protection).
func TestReconcilerMinAgeGuard(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	id := uuid.New()
	_, alive := admitWithAlive(t, backend, id)
	alive.Store(false)

	backend.reconcilePass(time.Hour, make(map[uuid.UUID]time.Time))
	if backend.Sessions.Get(id) == nil {
		t.Fatal("reconciler departed a session younger than min-age")
	}
}

// TestReconcilerOrphanConverges: derived state with no registry entry —
// maps, tracked keys, a cached entity marker — is swept via a synthetic
// entry, with the same wire output, after the two-pass aging.
func TestReconcilerOrphanConverges(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	fs := newFakeSpace()
	backend.ISpace = fs

	// Seed an orphan: bouncer in the map + cached state + entity
	// marker, but no registry entry (simulates derived state surviving
	// a lost departure).
	id := uuid.New()
	addTestBouncer(backend, id)
	sendOp(t, backend, "attributes", id.String()+".name", "ghost", id)
	sendOp(t, backend, "entity", id.String(), true, id)
	waitForCacheSet(t, backend, "entity", id.String())
	fs.AddNodeStyledWithId(id, "ghost", space.Position{}, common.SpaceNodeConfig{})

	// Registered observer — must not itself be treated as an orphan.
	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)
	backend.Sessions.Register(observerID, &sessions.FuncSession{}, "observer")
	firstSeen := make(map[uuid.UUID]time.Time)

	// Pass 1: records firstSeen, must not act.
	backend.reconcilePass(50*time.Millisecond, firstSeen)
	if _, ok := firstSeen[id]; !ok {
		t.Fatal("orphan not recorded as first-seen")
	}
	backend.Lock()
	_, stillThere := backend.BouncersByUuid[id]
	backend.Unlock()
	if !stillThere {
		t.Fatal("reconciler acted on first sight of the orphan")
	}

	// Pass 2 after the min-age: swept.
	time.Sleep(60 * time.Millisecond)
	backend.reconcilePass(50*time.Millisecond, firstSeen)

	backend.Lock()
	_, bOK := backend.BouncersByUuid[id]
	_, kOK := backend.ConnKeysBySubject[id]
	backend.Unlock()
	if bOK || kOK {
		t.Error("orphaned maps not released")
	}
	waitForTombstone(t, backend, "entity", id.String())
	waitForTombstone(t, backend, "attributes", id.String()+".name")
	if !waitFor(time.Second, func() bool {
		_, _, g := sweepCounts(observer)
		return g == 1
	}) {
		t.Error("orphan sweep did not announce Gone")
	}
	// Node removed from the space (DELETE enqueued by the departure).
	if n, _ := fs.GetNode(id); n != nil {
		t.Error("orphan node still in space")
	}
	// Synthetic entry not left behind.
	if backend.Sessions.Get(id) != nil {
		t.Error("synthetic entry still registered")
	}
}

// TestRegisterOrphanCollision: a real session registering between
// detection and action makes RegisterOrphan refuse — the reconciler
// cannot displace it.
func TestRegisterOrphanCollision(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	id := uuid.New()
	_, real := backend.Sessions.Register(id, &sessions.FuncSession{}, "test")
	if e := backend.Sessions.RegisterOrphan(id, "orphan"); e != nil {
		t.Fatal("RegisterOrphan displaced a real entry")
	}
	if backend.Sessions.Get(id) != real {
		t.Fatal("real entry no longer current")
	}
}

// TestReconcilerRacesEventDeparture: the pass and the owner's Stop fire
// together — exactly one sweep.
func TestReconcilerRacesEventDeparture(t *testing.T) {
	for round := 0; round < 10; round++ {
		backend := newTestBackend()
		backend.ISpace = newFakeSpace()

		id := uuid.New()
		handler, alive := admitWithAlive(t, backend, id)
		sendOp(t, backend, "entity", id.String(), true, id)
		waitForCacheSet(t, backend, "entity", id.String())
		_, observer := addTestBouncer(backend, uuid.New())

		alive.Store(false)
		done := make(chan struct{})
		go func() {
			backend.reconcilePass(0, make(map[uuid.UUID]time.Time))
			close(done)
		}()
		handler.Stop()
		<-done

		if !waitFor(time.Second, func() bool {
			_, e, g := sweepCounts(observer)
			return e >= 1 && g >= 1
		}) {
			t.Fatal("no sweep arrived")
		}
		time.Sleep(30 * time.Millisecond)
		_, e, g := sweepCounts(observer)
		if e != 1 || g != 1 {
			t.Fatalf("round %d: sweep counts entity:%d gone:%d, want 1/1", round, e, g)
		}
		backend.Stop()
	}
}

// TestNotifyStaleNodeFunnel: staleness Kills the transport; if the
// funnel completes, no escalation. If no owner reacts, the bounded
// escalation departs directly.
func TestNotifyStaleNodeFunnel(t *testing.T) {
	oldWait := staleEscalationWait
	staleEscalationWait = 80 * time.Millisecond
	defer func() { staleEscalationWait = oldWait }()

	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	// Case 1: Kill leads to an owner-driven departure (simulated).
	id1 := uuid.New()
	var killed atomic.Int32
	var h1 *ConnectionHandler
	live1 := &sessions.FuncSession{
		KillFn: func(string) {
			killed.Add(1)
			go h1.Stop() // the "owner goroutine" reacting to the sever
		},
	}
	h, serr := backend.NewConnectionHandlerWithError(noInputConfig(id1, "a"), nil, live1, "test")
	if serr != nil {
		t.Fatalf("admission failed: %v", serr)
	}
	h1 = h.(*ConnectionHandler)

	backend.NotifyStaleNode(id1)
	if !waitFor(time.Second, func() bool { return backend.Sessions.Get(id1) == nil }) {
		t.Fatal("stale funnel did not depart the session")
	}
	if killed.Load() != 1 {
		t.Errorf("Kill fired %d times, want 1", killed.Load())
	}

	// Case 2: Kill goes nowhere (no owner) — escalation departs.
	id2 := uuid.New()
	_, _ = admitWithAlive(t, backend, id2) // FuncSession with no KillFn effect
	backend.NotifyStaleNode(id2)
	// Repeated notifications (the tick fires every 5ms while stale)
	// must not multiply the escalation.
	for i := 0; i < 5; i++ {
		backend.NotifyStaleNode(id2)
	}
	if !waitFor(time.Second, func() bool { return backend.Sessions.Get(id2) == nil }) {
		t.Fatal("stale escalation did not depart the ownerless session")
	}
}

// TestReconcilerSoak: random connect/disconnect cycles with the
// reconciler running fast; at quiesce the derived sets match the live
// set exactly and goroutines are stable.
func TestReconcilerSoak(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()
	backend.StartReconciler(25 * time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()

	type liveNode struct {
		handler *ConnectionHandler
		alive   *atomic.Bool
	}
	nodes := make(map[uuid.UUID]*liveNode)

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 300; i++ {
		switch {
		case len(nodes) < 5 || rng.Intn(3) == 0: // connect
			id := uuid.New()
			h, alive := admitWithAlive(t, backend, id)
			nodes[id] = &liveNode{handler: h, alive: alive}
			sendOp(t, backend, "attributes", id.String()+".name", "x", id)
		default: // disconnect a random node, by a random mechanism
			for id, n := range nodes {
				switch rng.Intn(3) {
				case 0:
					n.handler.Stop()
				case 1:
					n.alive.Store(false) // reconciler's job
				case 2:
					backend.FreeSource(id) // timeout backstop
				}
				delete(nodes, id)
				break
			}
		}
	}
	// Stop the survivors through normal paths.
	for _, n := range nodes {
		n.handler.Stop()
	}

	// Quiesce: everything converges to empty.
	if !waitFor(5*time.Second, func() bool {
		if len(backend.Sessions.Snapshot()) != 0 {
			return false
		}
		backend.Lock()
		empty := len(backend.HandlersByUuid) == 0 && len(backend.BouncersByUuid) == 0 &&
			len(backend.ConnKeysBySubject) == 0
		backend.Unlock()
		return empty
	}) {
		backend.Lock()
		t.Fatalf("did not converge: sessions=%d handlers=%d bouncers=%d keys=%d",
			len(backend.Sessions.Snapshot()), len(backend.HandlersByUuid),
			len(backend.BouncersByUuid), len(backend.ConnKeysBySubject))
	}

	// Goroutine stability.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= baseline+3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("goroutines grew: baseline %d, now %d", baseline, runtime.NumGoroutine())
}
