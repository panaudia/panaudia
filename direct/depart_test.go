package direct

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/statecache"
)

// admitTestNode runs a full admission through the factory with a
// FuncSession whose Kill is recorded.
func admitTestNode(t *testing.T, backend *DirectBackend, id uuid.UUID) (*ConnectionHandler, *sessions.FuncSession, *atomic.Int32) {
	t.Helper()
	var kills atomic.Int32
	live := &sessions.FuncSession{
		KillFn: func(string) { kills.Add(1) },
	}
	h, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "node"), nil, live, "test")
	if serr != nil || h == nil {
		t.Fatalf("admission failed: %v", serr)
	}
	return h.(*ConnectionHandler), live, &kills
}

// sweepCounts counts departure-sweep envelopes per topic seen by an
// observing sender. Only tombstone envelopes count: the observer may
// also catch late broadcasts of the test's own Set ops (the cache write
// that waitForCacheSet polls happens before the broadcast) and the
// session's periodic sendEntity/sendAttributes re-emits.
func sweepCounts(sender *testSender) (attrEnvelopes, entityEnvelopes, goneDatagrams int) {
	isTombstoneEnvelope := func(msg string) bool {
		env, err := statecache.Decode([]byte(msg))
		if err != nil {
			return false
		}
		ops, _, err := statecache.ParseOps(env.Value)
		if err != nil || len(ops) == 0 {
			return false
		}
		for _, op := range ops {
			if !op.Tombstone {
				return false
			}
		}
		return true
	}
	for _, m := range sender.getStringMsgs() {
		if !isTombstoneEnvelope(m.msg) {
			continue
		}
		switch m.topic {
		case "attributes":
			attrEnvelopes++
		case "entity":
			entityEnvelopes++
		}
	}
	for _, m := range sender.getDataMsgs() {
		// Only count Gone=1 — live sessions broadcast periodic
		// NodeInfo3 (Gone=0) on the same topic. NodeInfo3FromBytes
		// deliberately doesn't decode Gone (inbound client state must
		// not carry one), so read the wire field directly.
		if m.topic == "state" && len(m.msg) >= 48 && inout.DecodeInt32(m.msg[44:]) == 1 {
			goneDatagrams++
		}
	}
	return
}

// TestDepartNodeIdempotent: N concurrent DepartNode calls (mixing Stop,
// direct calls, and the FreeSource backstop) produce exactly one sweep —
// one attributes envelope, one entity envelope, one Gone datagram.
func TestDepartNodeIdempotent(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	id := uuid.New()
	handler, _, kills := admitTestNode(t, backend, id)

	// Give the node some state to sweep.
	sendOp(t, backend, "attributes", id.String()+".name", "n", id)
	sendOp(t, backend, "entity", id.String(), true, id)
	waitForCacheSet(t, backend, "entity", id.String())

	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			switch n % 3 {
			case 0:
				handler.Stop()
			case 1:
				backend.DepartNode(handler.registryEntry, ReasonTransportClosed)
			case 2:
				backend.FreeSource(id)
			}
		}(i)
	}
	wg.Wait()

	// Exactly one sweep: poll until it lands, then confirm no extras.
	if !waitFor(time.Second, func() bool {
		a, e, g := sweepCounts(observer)
		return a >= 1 && e >= 1 && g >= 1
	}) {
		t.Fatal("departure sweep never arrived")
	}
	time.Sleep(50 * time.Millisecond)
	a, e, g := sweepCounts(observer)
	if a != 1 || e != 1 || g != 1 {
		for i, m := range observer.getStringMsgs() {
			t.Logf("string msg %d: topic=%s msg=%.200s", i, m.topic, m.msg)
		}
		t.Errorf("sweep counts = attrs:%d entity:%d gone:%d, want 1/1/1", a, e, g)
	}
	if kills.Load() != 1 {
		t.Errorf("Kill fired %d times, want 1", kills.Load())
	}
	if backend.Sessions.Get(id) != nil {
		t.Error("entry still registered after departure")
	}
	backend.Lock()
	_, hOK := backend.HandlersByUuid[id]
	_, bOK := backend.BouncersByUuid[id]
	backend.Unlock()
	if hOK || bOK {
		t.Error("backend maps not released")
	}
}

// TestKickFunnel: a kick op arriving on the receive path only severs the
// transport (Kill) — cleanup runs when the owner calls Stop, exactly
// once, with the kick record itself untouched (policy-scoped).
func TestKickFunnel(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	target := uuid.New()
	moderator := uuid.New()
	handler, _, kills := admitTestNode(t, backend, target)

	// The kick op flows to the target's bouncer via the normal broadcast
	// path (entity topic, written by a moderator via the command path).
	sendOp(t, backend, "entity", target.String()+".kicked", time.Now().Add(time.Hour).UnixMilli(), moderator)

	if !waitFor(time.Second, func() bool { return kills.Load() == 1 }) {
		t.Fatal("kick did not Kill the session")
	}
	// Funnel: Kill alone performs no cleanup.
	if backend.Sessions.Get(target) == nil {
		t.Fatal("kick performed cleanup before the owner exited")
	}

	// Owner exit.
	handler.Stop()
	if backend.Sessions.Get(target) != nil {
		t.Error("departure did not run on owner exit")
	}
	// The kick record persists (policy-scoped).
	present, tomb := cacheState(backend, "entity", target.String()+".kicked")
	if !present || tomb {
		t.Errorf("kick record should persist (present=%v tombstoned=%v)", present, tomb)
	}
}

// TestResurrectionDefense: writes racing the departure cannot end up as
// the last word — the bouncer is severed before the tombstones are
// enqueued, so the key always ends tombstoned (or never lands at all).
func TestResurrectionDefense(t *testing.T) {
	for round := 0; round < 10; round++ {
		backend := newTestBackend()
		backend.ISpace = newFakeSpace()

		id := uuid.New()
		handler, _, _ := admitTestNode(t, backend, id)
		backend.Lock()
		bouncer := backend.BouncersByUuid[id]
		backend.Unlock()

		key := id.String() + ".name"
		sendOp(t, backend, "attributes", key, "first", id)
		waitForCacheSet(t, backend, "attributes", key)

		// Hammer writes through the session's bouncer while departing.
		done := make(chan struct{})
		go func() {
			defer close(done)
			op, _ := statecache.BuildOp(key, "resurrected")
			for i := 0; i < 200; i++ {
				bouncer.SendString("attributes", string(op))
			}
		}()
		handler.Stop()
		<-done

		// Let the dispatcher drain, then the latest state must be a
		// tombstone.
		waitForTombstone(t, backend, "attributes", key)
		backend.Stop()
	}
}

// TestAdmissionWaitsForDeparture: a same-uuid admission arriving while a
// departure is in flight waits for it and then succeeds — and the new
// session's fresh writes are not shadowed by the old tombstones.
func TestAdmissionWaitsForDeparture(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	fs := newFakeSpace()
	backend.ISpace = fs

	id := uuid.New()
	handler, _, _ := admitTestNode(t, backend, id)
	entry := handler.registryEntry

	// Hold the entry in Departing manually, start the reconnect, then
	// release by completing the departure.
	if !entry.TryBeginDeparture() {
		t.Fatal("CAS failed")
	}

	admitted := make(chan *common.ServerError, 1)
	go func() {
		h, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "second"), nil, &sessions.FuncSession{}, "test")
		if h != nil {
			admitted <- nil
		} else {
			admitted <- serr
		}
	}()

	// The admission must be blocked while the departure is in flight.
	select {
	case serr := <-admitted:
		t.Fatalf("admission did not wait for the in-flight departure (result: %v)", serr)
	case <-time.After(100 * time.Millisecond):
	}

	// Complete the departure: DepartNode no-ops on the lost CAS, so run
	// the equivalent completion by hand (the real owner does this via
	// DepartNode's winning path).
	backend.ISpace.DeleteNode(id)
	backend.Lock()
	delete(backend.HandlersByUuid, id)
	if b, ok := backend.BouncersByUuid[id]; ok {
		b.Stop()
		delete(backend.BouncersByUuid, id)
	}
	backend.Unlock()
	backend.Sessions.Unregister(entry)
	entry.MarkDeparted()

	select {
	case serr := <-admitted:
		if serr != nil {
			t.Fatalf("admission failed after departure completed: %v", serr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admission still blocked after the departure completed")
	}

	// New session installed and registered.
	if e := backend.Sessions.Get(id); e == nil || e.State() != sessions.Live {
		t.Fatal("new session not live in registry")
	}
	// Fresh writes land normally (not shadowed).
	sendOp(t, backend, "attributes", id.String()+".name", "fresh", id)
	waitForCacheSet(t, backend, "attributes", id.String()+".name")
}

// TestAdmissionTimeoutOnStuckDeparture: a departure that never completes
// rejects the reconnect after the bounded wait instead of hanging it.
func TestAdmissionTimeoutOnStuckDeparture(t *testing.T) {
	oldTimeout := admissionWaitTimeout
	admissionWaitTimeout = 100 * time.Millisecond
	defer func() { admissionWaitTimeout = oldTimeout }()

	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	id := uuid.New()
	handler, _, _ := admitTestNode(t, backend, id)
	if !handler.registryEntry.TryBeginDeparture() {
		t.Fatal("CAS failed")
	}
	// Departure never completes.
	h2, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "second"), nil, &sessions.FuncSession{}, "test")
	if h2 != nil || serr == nil || serr.Code != common.SERVER_ERROR_DUPLICATE {
		t.Fatalf("expected bounded-wait DUPLICATE rejection, got h=%v serr=%v", h2, serr)
	}
}

// TestRapidReconnectFullCycle: disconnect/reconnect through the real
// paths — DepartNode then immediate re-admission — and the observer sees
// exactly one departure sweep followed by the new session working.
func TestRapidReconnectFullCycle(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	fs := newFakeSpace()
	backend.ISpace = fs

	id := uuid.New()
	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)

	handler, _, _ := admitTestNode(t, backend, id)
	sendOp(t, backend, "attributes", id.String()+".name", "before", id)
	waitForCacheSet(t, backend, "attributes", id.String()+".name")

	// Disconnect + immediate reconnect.
	handler.Stop()
	h2, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "again"), nil, &sessions.FuncSession{}, "test")
	if serr != nil || h2 == nil {
		t.Fatalf("rapid reconnect rejected: %v", serr)
	}
	defer h2.Stop()

	// The new session's writes land after the old tombstones.
	sendOp(t, backend, "attributes", id.String()+".name", "after", id)
	waitForCacheSet(t, backend, "attributes", id.String()+".name")

	// Node present in the space (DELETE then ADD, FIFO).
	if n, _ := fs.GetNode(id); n == nil {
		t.Error("node missing from space after reconnect")
	}
	// Exactly one Gone datagram (the departure), not two.
	_, _, gone := sweepCounts(observer)
	if gone != 1 {
		t.Errorf("observer saw %d Gone datagrams, want 1", gone)
	}
}

// TestDepartSoakNoGoroutineGrowth: 100 admit/depart cycles leave no
// goroutine growth (bouncer dispatch + sendInfo loops all exit).
func TestDepartSoakNoGoroutineGrowth(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	// Warm up & settle.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 100; i++ {
		id := uuid.New()
		handler, _, _ := admitTestNode(t, backend, id)
		sendOp(t, backend, "attributes", id.String()+".name", "x", id)
		handler.Stop()
	}

	// sendInfo loops poll done at ~100 ms ticks; allow them to drain.
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
