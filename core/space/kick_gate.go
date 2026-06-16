package space

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/statecache"
)

// KickGate is the auth-time half of kick enforcement: it mirrors the
// current `entity`/`space` kick state from the cache and answers
// IsKicked queries from the deployment's Authoriser. The connection
// half (live disconnect of an already-attached session) lives on
// BouncerClient — see SetKickFn / maybeKick.
//
// Deadline values are stored as unix-millisecond integers (matching
// the wire shape produced by `core/commands/defs.go::kickExpiry`).
// 0 means "forever" — never expires through the sweeper.
//
// KickGate owns no concept of OpID or last-write-wins: it is just a
// reflection of cache state and the cache itself is LWW. Apply is
// idempotent under redundant deliveries (the same op set on the same
// key writes the same deadline).
type KickGate struct {
	mu     sync.RWMutex
	entity map[uuid.UUID]int64
	role   map[string]int64

	// emit is the closure the sweeper uses to broadcast tombstones for
	// expired entries. Spatial-mixer wires it to push onto
	// DirectBackend.StringChIn; cloud-mixer wires it to publish via a
	// gateway-process bouncer client. May be nil (sweeper still works,
	// just doesn't broadcast — useful in tests and for read-only
	// gateways that observe-but-don't-emit).
	emit func(topic string, encoded []byte)

	// nowMillis is the time source. Production callers leave this nil
	// (the sweeper / IsKicked use time.Now). Tests inject a fake clock
	// to exercise the expiry path without sleeping.
	nowMillis func() int64

	sweepStop chan struct{}
	sweepDone chan struct{}
}

// NewKickGate constructs an empty gate with the supplied broadcast
// closure. emit may be nil for tests / read-only consumers.
func NewKickGate(emit func(topic string, encoded []byte)) *KickGate {
	return &KickGate{
		entity: make(map[uuid.UUID]int64),
		role:   make(map[string]int64),
		emit:   emit,
	}
}

func (g *KickGate) now() int64 {
	if g.nowMillis != nil {
		return g.nowMillis()
	}
	return time.Now().UnixMilli()
}

// Apply consumes a single kick-relevant op. Topic and key shape mirror
// the catalog entries:
//
//   - entity / `{uuid}.kicked`           → entity[uuid] = deadline (or delete on tombstone)
//   - space  / `roles-kicked.{role}`     → role[role]   = deadline (or delete on tombstone)
//
// All other keys are ignored — the same Apply is called for every
// op on these topics by the deployment's tee.
//
// Convention for op.Value (matches Phase 4 wiring): JSON of the value
// field only (e.g. `1747584300000`), not the wrapping op envelope.
func (g *KickGate) Apply(topic string, op statecache.Op) {
	switch topic {
	case "entity":
		id, rest, ok := splitEntityKey(op.Key)
		if !ok || rest != "kicked" {
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if op.Tombstone {
			delete(g.entity, id)
			return
		}
		var deadline int64
		if !decodeOpValue(op, &deadline) {
			common.LogWarn("KickGate.Apply: parse failed for entity key %q", op.Key)
			return
		}
		g.entity[id] = deadline

	case "space":
		const prefix = "roles-kicked."
		if !strings.HasPrefix(op.Key, prefix) {
			return
		}
		role := op.Key[len(prefix):]
		if role == "" {
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if op.Tombstone {
			delete(g.role, role)
			return
		}
		var deadline int64
		if !decodeOpValue(op, &deadline) {
			common.LogWarn("KickGate.Apply: parse failed for space key %q", op.Key)
			return
		}
		g.role[role] = deadline
	}
}

// IsKicked returns true if id or any role in roles is currently
// kicked. The second return is the latest non-forever deadline that
// will release the user (zero time.Time if any kick is "forever" or
// if the user is not kicked at all — distinguish via the bool).
//
// Expired entries are treated as not kicked even if still in the
// in-memory map (the sweeper hasn't run yet, or the round-trip
// tombstone hasn't returned). Auth-time correctness does not depend
// on the sweeper running.
func (g *KickGate) IsKicked(id uuid.UUID, roles []string) (bool, time.Time) {
	nowMs := g.now()
	g.mu.RLock()
	defer g.mu.RUnlock()

	var (
		kicked   bool
		forever  bool
		latestMs int64
	)

	if d, ok := g.entity[id]; ok {
		if d == 0 {
			kicked = true
			forever = true
		} else if d > nowMs {
			kicked = true
			if d > latestMs {
				latestMs = d
			}
		}
	}

	for _, r := range roles {
		d, ok := g.role[r]
		if !ok {
			continue
		}
		if d == 0 {
			kicked = true
			forever = true
		} else if d > nowMs {
			kicked = true
			if d > latestMs {
				latestMs = d
			}
		}
	}

	if forever {
		return true, time.Time{}
	}
	if kicked {
		return true, time.UnixMilli(latestMs)
	}
	return false, time.Time{}
}

// sweepOnce runs a single sweep tick: collects every entity / role
// entry whose deadline has passed (excluding forever-kicks at d==0),
// removes them from the maps, then emits one tombstone op per removed
// entry through the configured broadcaster.
//
// Exposed (lowercase only inside the package) so tests can drive
// expiry deterministically without spinning up the goroutine.
func (g *KickGate) sweepOnce() {
	nowMs := g.now()

	var (
		entityExpired []uuid.UUID
		roleExpired   []string
	)

	g.mu.Lock()
	for id, d := range g.entity {
		if d != 0 && d <= nowMs {
			entityExpired = append(entityExpired, id)
		}
	}
	for r, d := range g.role {
		if d != 0 && d <= nowMs {
			roleExpired = append(roleExpired, r)
		}
	}
	for _, id := range entityExpired {
		delete(g.entity, id)
	}
	for _, r := range roleExpired {
		delete(g.role, r)
	}
	emit := g.emit
	g.mu.Unlock()

	if emit == nil {
		return
	}
	for _, id := range entityExpired {
		tb, err := statecache.BuildTombstoneOp(id.String() + ".kicked")
		if err != nil {
			common.LogWarn("KickGate.sweepOnce: build tombstone for %s: %v", id, err)
			continue
		}
		emit("entity", tb)
	}
	for _, r := range roleExpired {
		tb, err := statecache.BuildTombstoneOp("roles-kicked." + r)
		if err != nil {
			common.LogWarn("KickGate.sweepOnce: build tombstone for role %q: %v", r, err)
			continue
		}
		emit("space", tb)
	}
}

// StartSweeper launches the periodic sweeper goroutine. interval
// should be small enough that expired kicks are released promptly
// (typically 1s). No-op if the sweeper is already running.
func (g *KickGate) StartSweeper(interval time.Duration) {
	g.mu.Lock()
	if g.sweepStop != nil {
		g.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	g.sweepStop = stop
	g.sweepDone = done
	g.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				g.sweepOnce()
			}
		}
	}()
}

// StopSweeper signals the sweeper goroutine to exit and waits for
// it to drain. Safe to call from any goroutine; safe to call when no
// sweeper is running.
func (g *KickGate) StopSweeper() {
	g.mu.Lock()
	stop := g.sweepStop
	done := g.sweepDone
	g.sweepStop = nil
	g.sweepDone = nil
	g.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
}
