// Package sessions is the liveness authority for client sessions: a
// registry of every admitted session keyed by node uuid (= ticket jti),
// each entry carrying a per-instance generation and a
// Live → Departing → Departed lifecycle state machine.
//
// Phase 2 of plan/history/state-cleanup/plan.md: transports register/unregister
// here passively (nothing reads the registry for decisions yet). The
// departNode funnel (phase 3), reconciler (phase 4), and same-jti
// eviction (phase 5) build on it; the CAS state machine replaces the
// scattered stopOnce/isActive guards there.
package sessions

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
)

// LiveSession is a transport's handle on one client session.
type LiveSession interface {
	// Alive reports whether the transport believes the session is up.
	// Must be cheap; called by the reconciler on every pass.
	Alive() bool
	// Kill forcibly closes the transport. Idempotent. Kill is the ONLY
	// closer of the transport under the funnel discipline
	// (mechanism-design §3a): every signal that wants a session gone
	// calls Kill; cleanup is triggered solely by the owner goroutine
	// observing the transport close.
	Kill(reason string)
}

// State is an Entry's lifecycle position.
type State int32

const (
	Live State = iota
	Departing
	Departed
)

func (s State) String() string {
	switch s {
	case Live:
		return "live"
	case Departing:
		return "departing"
	case Departed:
		return "departed"
	default:
		return "unknown"
	}
}

// Entry is one registered session instance. The state machine is per
// instance (per generation), not per uuid: uuids recur across
// reconnects, and a stale call holding an old entry must not be able to
// touch the successor session.
type Entry struct {
	Uuid       uuid.UUID
	Session    LiveSession
	Transport  string // "moq-wt", "moq-quic", "webrtc", "roc", "roc-out"
	Generation uint64
	AdmittedAt time.Time

	state    atomic.Int32
	departed chan struct{}
}

// State returns the entry's current lifecycle state.
func (e *Entry) State() State {
	return State(e.state.Load())
}

// TryBeginDeparture attempts the Live→Departing CAS. Exactly one caller
// wins; the winner runs the departure, losers either return or wait on
// Departed().
func (e *Entry) TryBeginDeparture() bool {
	return e.state.CompareAndSwap(int32(Live), int32(Departing))
}

// MarkDeparted moves the entry to Departed and releases all Departed()
// waiters. Idempotent.
func (e *Entry) MarkDeparted() {
	if e.state.Swap(int32(Departed)) != int32(Departed) {
		close(e.departed)
	}
}

// Departed returns a channel closed when the entry reaches Departed.
func (e *Entry) Departed() <-chan struct{} {
	return e.departed
}

// Registry maps node uuid → current session Entry. It is the single
// source of truth for "which sessions are live"; all derived state
// (space nodes, handlers, bouncers, tracked keys) reconciles against it.
type Registry struct {
	mu      sync.Mutex
	entries map[uuid.UUID]*Entry
	gen     atomic.Uint64
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[uuid.UUID]*Entry)}
}

// Register installs a new entry for the uuid and returns it, along with
// the displaced previous entry (nil if none). The caller decides what to
// do with a still-live old entry — in phase 2 that is log-only; phase 5
// makes it an eviction.
func (r *Registry) Register(id uuid.UUID, s LiveSession, transport string) (old *Entry, e *Entry) {
	e = &Entry{
		Uuid:       id,
		Session:    s,
		Transport:  transport,
		Generation: r.gen.Add(1),
		AdmittedAt: time.Now(),
		departed:   make(chan struct{}),
	}
	r.mu.Lock()
	old = r.entries[id]
	r.entries[id] = e
	r.mu.Unlock()

	common.LogInfo("[sessions] register uuid=%s gen=%d transport=%s", id, e.Generation, transport)
	if old != nil && old.State() == Live && old.Session.Alive() {
		// Field data for phase 5: how often do same-jti collisions
		// actually occur? No action yet.
		common.LogWarn("[sessions] same-identity collision: uuid=%s newGen=%d displaces live gen=%d (%s) — eviction lands in phase 5",
			id, e.Generation, old.Generation, old.Transport)
	}
	return old, e
}

// RegisterOrphan atomically installs an entry for a uuid that has NO
// current entry, returning nil if one exists. Used by the reconciler to
// synthesize a departure for orphaned derived state: the install is
// atomic with the no-entry check, so it cannot displace a real session
// that registered between the reconciler's detection and its action —
// and once installed, a concurrent admission for the uuid serializes
// behind the synthetic entry's departure like any other.
func (r *Registry) RegisterOrphan(id uuid.UUID, transport string) *Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[id] != nil {
		return nil
	}
	e := &Entry{
		Uuid:       id,
		Session:    &FuncSession{},
		Transport:  transport,
		Generation: r.gen.Add(1),
		AdmittedAt: time.Now(),
		departed:   make(chan struct{}),
	}
	r.entries[id] = e
	common.LogInfo("[sessions] register-orphan uuid=%s gen=%d (%s)", id, e.Generation, transport)
	return e
}

// Unregister removes the entry iff it is still the current one for its
// uuid (identity-checked): a slow old teardown cannot unregister its
// successor after a re-register.
func (r *Registry) Unregister(e *Entry) {
	if e == nil {
		return
	}
	r.mu.Lock()
	current := r.entries[e.Uuid]
	if current == e {
		delete(r.entries, e.Uuid)
	}
	r.mu.Unlock()
	if current == e {
		common.LogInfo("[sessions] unregister uuid=%s gen=%d transport=%s state=%s",
			e.Uuid, e.Generation, e.Transport, e.State())
	} else {
		common.LogDebug("[sessions] stale unregister ignored: uuid=%s gen=%d (current gen=%v)",
			e.Uuid, e.Generation, currentGen(current))
	}
}

func currentGen(e *Entry) interface{} {
	if e == nil {
		return "none"
	}
	return e.Generation
}

// Get returns the current entry for a uuid, or nil.
func (r *Registry) Get(id uuid.UUID) *Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[id]
}

// Snapshot returns the current entries (order unspecified).
func (r *Registry) Snapshot() []*Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out
}

// FuncSession adapts closures to LiveSession; Kill is single-fire.
type FuncSession struct {
	AliveFn  func() bool
	KillFn   func(reason string)
	killOnce sync.Once
}

func (s *FuncSession) Alive() bool {
	if s.AliveFn == nil {
		return true
	}
	return s.AliveFn()
}

func (s *FuncSession) Kill(reason string) {
	s.killOnce.Do(func() {
		if s.KillFn != nil {
			s.KillFn(reason)
		}
	})
}
