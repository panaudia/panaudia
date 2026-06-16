package sessions

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func liveStub() *FuncSession {
	return &FuncSession{AliveFn: func() bool { return true }}
}

// TestTryBeginDepartureCAS: N goroutines race the Live→Departing CAS;
// exactly one wins.
func TestTryBeginDepartureCAS(t *testing.T) {
	for round := 0; round < 50; round++ {
		r := NewRegistry()
		_, e := r.Register(uuid.New(), liveStub(), "test")

		const n = 16
		var wins atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if e.TryBeginDeparture() {
					wins.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("round %d: %d CAS winners, want exactly 1", round, wins.Load())
		}
		if e.State() != Departing {
			t.Fatalf("round %d: state %v after CAS, want Departing", round, e.State())
		}
	}
}

// TestDepartedReleasesAllWaiters: every goroutine blocked on Departed()
// is released by MarkDeparted, which is also idempotent.
func TestDepartedReleasesAllWaiters(t *testing.T) {
	r := NewRegistry()
	_, e := r.Register(uuid.New(), liveStub(), "test")

	const n = 8
	var released atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-e.Departed()
			released.Add(1)
		}()
	}

	if !e.TryBeginDeparture() {
		t.Fatal("CAS failed on a fresh entry")
	}
	e.MarkDeparted()
	e.MarkDeparted() // idempotent: no double-close panic

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("only %d/%d waiters released", released.Load(), n)
	}
	if e.State() != Departed {
		t.Fatalf("state %v, want Departed", e.State())
	}
}

// TestMarkDepartedWithoutCAS: MarkDeparted from Live also releases (the
// guard is the Swap, not the CAS path).
func TestMarkDepartedWithoutCAS(t *testing.T) {
	r := NewRegistry()
	_, e := r.Register(uuid.New(), liveStub(), "test")
	e.MarkDeparted()
	select {
	case <-e.Departed():
	default:
		t.Fatal("Departed channel not closed")
	}
}

// TestIdentityCheckedUnregister: a stale entry cannot remove its
// successor after a same-uuid re-register.
func TestIdentityCheckedUnregister(t *testing.T) {
	r := NewRegistry()
	id := uuid.New()

	_, first := r.Register(id, liveStub(), "test")
	old, second := r.Register(id, liveStub(), "test")
	if old != first {
		t.Fatal("Register did not return the displaced entry")
	}

	// Stale teardown of the first session.
	r.Unregister(first)
	if got := r.Get(id); got != second {
		t.Fatalf("stale Unregister removed the successor (got %v)", got)
	}

	// The successor's own unregister works.
	r.Unregister(second)
	if got := r.Get(id); got != nil {
		t.Fatalf("entry still present after current unregister: %v", got)
	}

	// Unregister of an already-removed / nil entry is a no-op.
	r.Unregister(second)
	r.Unregister(nil)
}

// TestGenerationMonotonic: generations strictly increase across
// re-registers of the same uuid (and across uuids).
func TestGenerationMonotonic(t *testing.T) {
	r := NewRegistry()
	id := uuid.New()

	var last uint64
	for i := 0; i < 10; i++ {
		_, e := r.Register(id, liveStub(), "test")
		if e.Generation <= last {
			t.Fatalf("generation %d not > previous %d", e.Generation, last)
		}
		last = e.Generation
		r.Unregister(e)
	}
}

// TestSnapshot: snapshot reflects current entries only.
func TestSnapshot(t *testing.T) {
	r := NewRegistry()
	_, a := r.Register(uuid.New(), liveStub(), "test")
	_, b := r.Register(uuid.New(), liveStub(), "test")

	if got := len(r.Snapshot()); got != 2 {
		t.Fatalf("snapshot size %d, want 2", got)
	}
	r.Unregister(a)
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0] != b {
		t.Fatalf("snapshot after unregister = %v, want [b]", snap)
	}
}

// TestFuncSessionKillOnce: Kill fires the closure exactly once.
func TestFuncSessionKillOnce(t *testing.T) {
	var calls atomic.Int32
	s := &FuncSession{KillFn: func(string) { calls.Add(1) }}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Kill("test") }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("KillFn called %d times, want 1", calls.Load())
	}
}
