package sessions

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestAdmitFresh: no entry → immediate OK, nothing else.
func TestAdmitFresh(t *testing.T) {
	r := NewRegistry()
	out := r.Admit(uuid.New(), 50*time.Millisecond,
		func(*Entry) { t.Error("kill called") },
		func(*Entry) { t.Error("forceDepart called") })
	if !out.OK || out.Waited || out.Evicted {
		t.Fatalf("outcome = %+v, want OK only", out)
	}
}

// TestAdmitEvictsLive: a live old entry is killed; the simulated owner
// departs; Admit reports evicted+waited.
func TestAdmitEvictsLive(t *testing.T) {
	r := NewRegistry()
	id := uuid.New()
	var old *Entry
	live := &FuncSession{KillFn: func(string) {
		go func() {
			if old.TryBeginDeparture() {
				r.Unregister(old)
				old.MarkDeparted()
			}
		}()
	}}
	_, old = r.Register(id, live, "test")

	out := r.Admit(id, time.Second,
		func(e *Entry) { e.Session.Kill("evicted") },
		func(*Entry) { t.Error("forceDepart called despite responsive owner") })
	if !out.OK || !out.Waited || !out.Evicted {
		t.Fatalf("outcome = %+v, want OK+Waited+Evicted", out)
	}
}

// TestAdmitWaitsOnDeparting: an in-flight departure is waited out, no
// eviction.
func TestAdmitWaitsOnDeparting(t *testing.T) {
	r := NewRegistry()
	id := uuid.New()
	_, old := r.Register(id, &FuncSession{}, "test")
	if !old.TryBeginDeparture() {
		t.Fatal("CAS failed")
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		r.Unregister(old)
		old.MarkDeparted()
	}()

	out := r.Admit(id, time.Second,
		func(*Entry) { t.Error("kill called on a departing entry") },
		func(*Entry) { t.Error("forceDepart called") })
	if !out.OK || !out.Waited || out.Evicted {
		t.Fatalf("outcome = %+v, want OK+Waited", out)
	}
}

// TestAdmitForceDepartFallback: an unresponsive owner triggers the
// forceDepart escalation; admission succeeds once it completes.
func TestAdmitForceDepartFallback(t *testing.T) {
	r := NewRegistry()
	id := uuid.New()
	r.Register(id, &FuncSession{}, "test") // Kill is a no-op

	var forced atomic.Int32
	out := r.Admit(id, 50*time.Millisecond,
		func(e *Entry) { e.Session.Kill("evicted") },
		func(e *Entry) {
			forced.Add(1)
			if e.TryBeginDeparture() {
				r.Unregister(e)
				e.MarkDeparted()
			}
		})
	if !out.OK || !out.Waited || !out.Evicted {
		t.Fatalf("outcome = %+v, want OK+Waited+Evicted", out)
	}
	if forced.Load() != 1 {
		t.Errorf("forceDepart called %d times, want 1", forced.Load())
	}
}

// TestAdmitWedgedRejects: a departure that never completes (CAS held
// elsewhere, never marked departed) yields !OK after both waits.
func TestAdmitWedgedRejects(t *testing.T) {
	r := NewRegistry()
	id := uuid.New()
	_, old := r.Register(id, &FuncSession{}, "test")
	old.TryBeginDeparture() // wedged: never completes

	out := r.Admit(id, 30*time.Millisecond,
		func(*Entry) {},
		func(*Entry) {}) // forceDepart loses the CAS, does nothing
	if out.OK {
		t.Fatalf("outcome = %+v, want !OK", out)
	}
}
