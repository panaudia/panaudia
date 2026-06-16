package space

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/statecache"
)

// fakeClock returns a fixed millis value, swappable atomically for
// fast-forward in tests.
type fakeClock struct {
	ms atomic.Int64
}

func newFakeClock(start int64) *fakeClock {
	c := &fakeClock{}
	c.ms.Store(start)
	return c
}

func (c *fakeClock) now() int64       { return c.ms.Load() }
func (c *fakeClock) advance(ms int64) { c.ms.Add(ms) }
func (c *fakeClock) set(ms int64)     { c.ms.Store(ms) }

// recorder accumulates emit() calls so tests can inspect the broadcast
// stream without a live bouncer.
type recorder struct {
	mu   sync.Mutex
	sent []recorded
}

type recorded struct {
	topic   string
	payload []byte
}

func (r *recorder) emit(topic string, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	r.sent = append(r.sent, recorded{topic: topic, payload: cp})
}

func (r *recorder) snapshot() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recorded, len(r.sent))
	copy(out, r.sent)
	return out
}

// applyEntityKick is a small helper that constructs the JSON value
// bytes that arrive on the Apply path (per Phase 4 convention: only
// the value field's JSON, not the full envelope).
func entityKickOp(id uuid.UUID, deadlineMs int64) statecache.Op {
	value, _ := json.Marshal(deadlineMs)
	return statecache.Op{
		Topic: "entity",
		Key:   id.String() + ".kicked",
		Value: value,
		OpID:  1,
	}
}

func entityUnkickOp(id uuid.UUID) statecache.Op {
	return statecache.Op{
		Topic:     "entity",
		Key:       id.String() + ".kicked",
		Tombstone: true,
	}
}

func roleKickOp(role string, deadlineMs int64) statecache.Op {
	value, _ := json.Marshal(deadlineMs)
	return statecache.Op{
		Topic: "space",
		Key:   "roles-kicked." + role,
		Value: value,
	}
}

func roleUnkickOp(role string) statecache.Op {
	return statecache.Op{
		Topic:     "space",
		Key:       "roles-kicked." + role,
		Tombstone: true,
	}
}

func TestKickGateIsKickedNoEntries(t *testing.T) {
	g := NewKickGate(nil)
	id := uuid.New()
	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("empty gate must report not kicked")
	}
}

func TestKickGateIsKickedEntityOnly(t *testing.T) {
	clk := newFakeClock(1_000)
	g := NewKickGate(nil)
	g.nowMillis = clk.now

	id := uuid.New()
	other := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))

	if got, deadline := g.IsKicked(id, nil); !got || deadline != time.UnixMilli(5_000) {
		t.Fatalf("entity should be kicked until 5000ms, got (%v, %v)", got, deadline)
	}
	if got, _ := g.IsKicked(other, nil); got {
		t.Fatalf("other id should not be kicked")
	}
}

func TestKickGateIsKickedRoleOnly(t *testing.T) {
	clk := newFakeClock(1_000)
	g := NewKickGate(nil)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("space", roleKickOp("performer", 10_000))

	if got, deadline := g.IsKicked(id, []string{"performer"}); !got || deadline != time.UnixMilli(10_000) {
		t.Fatalf("performer role should be kicked until 10000ms, got (%v, %v)", got, deadline)
	}
	if got, _ := g.IsKicked(id, []string{"audience"}); got {
		t.Fatalf("audience-only user should not be kicked")
	}
	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("no-roles user should not be kicked by role-only gate")
	}
}

// IsKicked must return the *latest* deadline across all relevant
// entries — that's the time at which the user becomes unkicked.
func TestKickGateIsKickedLatestDeadlineWins(t *testing.T) {
	clk := newFakeClock(1_000)
	g := NewKickGate(nil)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))
	g.Apply("space", roleKickOp("performer", 10_000))

	if got, deadline := g.IsKicked(id, []string{"performer"}); !got || deadline != time.UnixMilli(10_000) {
		t.Fatalf("latest deadline should win, got (%v, %v)", got, deadline)
	}

	// Reverse order: entity later than role.
	g2 := NewKickGate(nil)
	g2.nowMillis = clk.now
	g2.Apply("entity", entityKickOp(id, 20_000))
	g2.Apply("space", roleKickOp("performer", 10_000))
	if got, deadline := g2.IsKicked(id, []string{"performer"}); !got || deadline != time.UnixMilli(20_000) {
		t.Fatalf("entity deadline should win when later, got (%v, %v)", got, deadline)
	}
}

// A deadline of 0 means "forever" — bool is true, time.Time is zero,
// and any future `now` keeps the user kicked.
func TestKickGateIsKickedForever(t *testing.T) {
	clk := newFakeClock(1_000)
	g := NewKickGate(nil)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 0))

	if got, deadline := g.IsKicked(id, nil); !got || !deadline.IsZero() {
		t.Fatalf("forever kick should return (true, zero), got (%v, %v)", got, deadline)
	}
	clk.set(1_000_000_000_000)
	if got, _ := g.IsKicked(id, nil); !got {
		t.Fatalf("forever kick should still be kicked far in the future")
	}
}

// "forever" beats any finite deadline in the latest-deadline calc:
// the second return is zero-time so callers know it's permanent.
func TestKickGateIsKickedForeverBeatsFinite(t *testing.T) {
	g := NewKickGate(nil)
	g.nowMillis = newFakeClock(1_000).now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))
	g.Apply("space", roleKickOp("performer", 0)) // forever

	got, deadline := g.IsKicked(id, []string{"performer"})
	if !got || !deadline.IsZero() {
		t.Fatalf("forever role kick should make IsKicked return (true, zero), got (%v, %v)", got, deadline)
	}
}

// Expired entries (deadline ≤ now) are treated as not kicked, even if
// the sweeper hasn't yet removed them from the in-memory map.
func TestKickGateIsKickedExpiredIgnored(t *testing.T) {
	clk := newFakeClock(1_000)
	g := NewKickGate(nil)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))

	clk.set(5_000)
	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("entry at exactly now must be treated as expired")
	}
	clk.set(6_000)
	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("expired entity entry should not count as kicked")
	}
}

// Tombstone clears the entry — IsKicked drops to false immediately.
func TestKickGateApplyTombstoneClears(t *testing.T) {
	clk := newFakeClock(1_000)
	g := NewKickGate(nil)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))
	g.Apply("entity", entityUnkickOp(id))
	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("tombstone must clear entity kick")
	}

	g.Apply("space", roleKickOp("performer", 5_000))
	g.Apply("space", roleUnkickOp("performer"))
	if got, _ := g.IsKicked(id, []string{"performer"}); got {
		t.Fatalf("tombstone must clear role kick")
	}
}

// Apply ignores keys that aren't kick keys (forward-compat: future
// keys on the entity / space topic must not crash or pollute the
// gate's maps).
func TestKickGateApplyIgnoresOtherKeys(t *testing.T) {
	g := NewKickGate(nil)

	id := uuid.New()
	g.Apply("entity", statecache.Op{
		Topic: "entity",
		Key:   id.String() + ".gain",
		Value: []byte("0.5"),
	})
	g.Apply("space", statecache.Op{
		Topic: "space",
		Key:   "roles-muted.performer",
		Value: []byte("true"),
	})

	if got, _ := g.IsKicked(id, []string{"performer"}); got {
		t.Fatalf("non-kick keys must not register as kicks")
	}
}

// Sweeper emits exactly one tombstone per expired entity entry, and
// clears the in-memory entry afterward.
func TestKickGateSweepEmitsTombstoneAndClears(t *testing.T) {
	clk := newFakeClock(1_000)
	rec := &recorder{}
	g := NewKickGate(rec.emit)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))

	// Before expiry — sweeper does nothing.
	g.sweepOnce()
	if len(rec.snapshot()) != 0 {
		t.Fatalf("sweeper should not emit before expiry")
	}

	clk.set(5_001)
	g.sweepOnce()

	sent := rec.snapshot()
	if len(sent) != 1 {
		t.Fatalf("expected 1 tombstone, got %d", len(sent))
	}
	if sent[0].topic != "entity" {
		t.Fatalf("tombstone topic = %q, want entity", sent[0].topic)
	}
	ops, _, err := statecache.ParseOps(sent[0].payload)
	if err != nil {
		t.Fatalf("ParseOps: %v", err)
	}
	if len(ops) != 1 || !ops[0].Tombstone || ops[0].Key != id.String()+".kicked" {
		t.Fatalf("unexpected tombstone op: %+v", ops)
	}

	// In-memory entry is gone.
	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("expired entry should be cleared after sweep")
	}

	// A second sweep must NOT re-emit.
	g.sweepOnce()
	if len(rec.snapshot()) != 1 {
		t.Fatalf("second sweep should not re-emit, got %d total", len(rec.snapshot()))
	}
}

// Same for role kicks — emit on the space topic, key shape is `roles-kicked.{R}`.
func TestKickGateSweepEmitsTombstoneForRole(t *testing.T) {
	clk := newFakeClock(1_000)
	rec := &recorder{}
	g := NewKickGate(rec.emit)
	g.nowMillis = clk.now

	g.Apply("space", roleKickOp("performer", 5_000))
	clk.set(6_000)
	g.sweepOnce()

	sent := rec.snapshot()
	if len(sent) != 1 || sent[0].topic != "space" {
		t.Fatalf("expected 1 tombstone on space topic, got %+v", sent)
	}
	ops, _, _ := statecache.ParseOps(sent[0].payload)
	if len(ops) != 1 || !ops[0].Tombstone || ops[0].Key != "roles-kicked.performer" {
		t.Fatalf("unexpected role tombstone: %+v", ops)
	}
}

// Forever kicks are never swept — the sweeper only acts on
// finite-deadline entries.
func TestKickGateSweepLeavesForeverAlone(t *testing.T) {
	clk := newFakeClock(1_000)
	rec := &recorder{}
	g := NewKickGate(rec.emit)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 0))

	clk.set(1_000_000_000_000)
	g.sweepOnce()

	if len(rec.snapshot()) != 0 {
		t.Fatalf("sweeper must not emit for forever kick, got %d", len(rec.snapshot()))
	}
	if got, _ := g.IsKicked(id, nil); !got {
		t.Fatalf("forever kick should still be kicked after sweep")
	}
}

// Round-trip: Apply(set) → IsKicked true → fast-forward → sweepOnce
// emits tombstone → simulate the central bouncer echoing it back via
// Apply(tombstone) → IsKicked false.
func TestKickGateRoundTrip(t *testing.T) {
	clk := newFakeClock(1_000)
	rec := &recorder{}
	g := NewKickGate(rec.emit)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))
	if got, _ := g.IsKicked(id, nil); !got {
		t.Fatalf("expected kicked after Apply")
	}

	clk.set(6_000)
	g.sweepOnce()

	sent := rec.snapshot()
	if len(sent) != 1 {
		t.Fatalf("expected sweeper to emit once")
	}

	// Round-trip: the broadcast is parsed back into an op and replayed
	// via Apply (mirrors the path through the cache → broadcast loop).
	ops, _, _ := statecache.ParseOps(sent[0].payload)
	if len(ops) != 1 {
		t.Fatalf("expected single op")
	}
	g.Apply("entity", statecache.Op{
		Topic:     "entity",
		Key:       ops[0].Key,
		Tombstone: true,
	})
	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("after round-trip tombstone, expected not kicked")
	}
}

// Sweeper goroutine: StartSweeper / StopSweeper without nil-emit must
// not deadlock or leak.
func TestKickGateSweeperGoroutine(t *testing.T) {
	g := NewKickGate(nil)
	g.StartSweeper(5 * time.Millisecond)
	// Calling start again is a no-op (no second goroutine spun up).
	g.StartSweeper(5 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	g.StopSweeper()
	// Stop is idempotent.
	g.StopSweeper()
}

// Negative case: emit nil on a sweep that finds expirations — the
// in-memory entries are still cleared, just no broadcast.
func TestKickGateSweepWithNilEmit(t *testing.T) {
	clk := newFakeClock(1_000)
	g := NewKickGate(nil)
	g.nowMillis = clk.now

	id := uuid.New()
	g.Apply("entity", entityKickOp(id, 5_000))
	clk.set(6_000)
	g.sweepOnce()

	if got, _ := g.IsKicked(id, nil); got {
		t.Fatalf("nil-emit sweep should still clear expired entries")
	}
}
