package direct

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/core/statecache"
)

// waitFor polls fn until it returns true or the deadline expires.
// Returns true if fn ever returned true.
func waitFor(deadline time.Duration, fn func() bool) bool {
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		if fn() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fn()
}

// TestKickGateTee_EntityKick verifies an entity-kick op pushed onto
// StringChIn lands in the backend's KickGate; IsKicked then returns
// true for the targeted user.
func TestKickGateTee_EntityKick(t *testing.T) {
	backend := newTestBackend()
	backend.KickGate = space.NewKickGate(nil)
	t.Cleanup(func() { close(backend.Quit) })

	id := uuid.New()
	op, err := statecache.BuildOp(id.String()+".kicked", time.Now().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	backend.StringChIn <- StringMessage{topic: "entity", msg: string(op), sourceUUID: uuid.New()}

	if !waitFor(time.Second, func() bool {
		kicked, _ := backend.KickGate.IsKicked(id, nil)
		return kicked
	}) {
		t.Fatalf("KickGate did not see entity kick within 1s")
	}
}

// TestKickGateTee_RoleKick verifies a role-kick op flows to the gate
// and IsKicked returns true for any user holding that role.
func TestKickGateTee_RoleKick(t *testing.T) {
	backend := newTestBackend()
	backend.KickGate = space.NewKickGate(nil)
	t.Cleanup(func() { close(backend.Quit) })

	op, err := statecache.BuildOp("roles-kicked.performer", time.Now().Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	backend.StringChIn <- StringMessage{topic: "space", msg: string(op), sourceUUID: uuid.New()}

	other := uuid.New()
	if !waitFor(time.Second, func() bool {
		kicked, _ := backend.KickGate.IsKicked(other, []string{"performer"})
		return kicked
	}) {
		t.Fatalf("KickGate did not see role kick within 1s")
	}
}

// TestKickGateTee_TombstoneClears verifies an unkick (tombstone) flows
// to the gate and clears the prior kick.
func TestKickGateTee_TombstoneClears(t *testing.T) {
	backend := newTestBackend()
	backend.KickGate = space.NewKickGate(nil)
	t.Cleanup(func() { close(backend.Quit) })

	id := uuid.New()
	setOp, _ := statecache.BuildOp(id.String()+".kicked", time.Now().Add(time.Hour).UnixMilli())
	backend.StringChIn <- StringMessage{topic: "entity", msg: string(setOp), sourceUUID: uuid.New()}

	if !waitFor(time.Second, func() bool {
		kicked, _ := backend.KickGate.IsKicked(id, nil)
		return kicked
	}) {
		t.Fatalf("KickGate did not see initial kick")
	}

	tombOp, _ := statecache.BuildTombstoneOp(id.String() + ".kicked")
	backend.StringChIn <- StringMessage{topic: "entity", msg: string(tombOp), sourceUUID: uuid.New()}

	if !waitFor(time.Second, func() bool {
		kicked, _ := backend.KickGate.IsKicked(id, nil)
		return !kicked
	}) {
		t.Fatalf("KickGate did not clear after tombstone")
	}
}

// TestKickGateTee_NilGateNoOp confirms the tee is fully optional —
// a backend with no gate ingests entity/space ops without panicking.
// Snapshot proves the op landed; the absence of a panic proves the
// nil-gate guard fires cleanly.
func TestKickGateTee_NilGateNoOp(t *testing.T) {
	backend := newTestBackend() // no KickGate
	t.Cleanup(func() { close(backend.Quit) })

	id := uuid.New()
	op, _ := statecache.BuildOp(id.String()+".kicked", time.Now().Add(time.Hour).UnixMilli())
	backend.StringChIn <- StringMessage{topic: "entity", msg: string(op), sourceUUID: uuid.New()}

	want := id.String() + ".kicked"
	if !waitFor(time.Second, func() bool {
		for _, cached := range backend.Cache.Snapshot() {
			if cached.Topic == "entity" && cached.Key == want && !cached.Tombstone {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("op never reached the cache; nil-gate path may have stalled")
	}
}
