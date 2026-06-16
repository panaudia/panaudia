package sessions

import (
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
)

// DepartReason labels why a session departed — logging/telemetry only;
// the wire output is identical for every reason (clean and abrupt are
// indistinguishable to other participants — plan/history/state-cleanup Q3).
// Lives here (rather than the spatial backend) so cloud-mixer backends
// share the vocabulary.
type DepartReason string

const (
	ReasonTransportClosed DepartReason = "transport-closed"
	ReasonTimeout         DepartReason = "timeout"
	ReasonKicked          DepartReason = "kicked"
	ReasonEvicted         DepartReason = "evicted"
	ReasonReconciler      DepartReason = "reconciler"
	ReasonShutdown        DepartReason = "shutdown"
	ReasonStopped         DepartReason = "stopped"
)

// AdmitOutcome reports what Admit did.
type AdmitOutcome struct {
	// OK: the identity is clear — proceed with the admission.
	OK bool
	// Waited: an old session's departure completed before we proceeded
	// (the caller should skip checks that assume no queued removal).
	Waited bool
	// Evicted: the old session was still live and was killed (Q4).
	Evicted bool
}

// Admit enforces single-session-per-identity at admission
// (plan/history/state-cleanup phases 3+5). If the registry holds an entry for
// the uuid: a still-Live entry is evicted — kill(old) must sever its
// transport so the funnel runs its full announced departure — and any
// in-flight departure is waited out (bounded). If the funnel doesn't
// complete within the wait, forceDepart(old) is invoked (the caller's
// DepartNode — CAS-safe: it completes synchronously when no owner ever
// reacted, and no-ops if a departure is mid-flight elsewhere), followed
// by one more bounded wait. Only a genuinely wedged departure yields
// !OK.
//
// The caller registers its own new entry after a true outcome; Admit
// itself does not register.
func (r *Registry) Admit(id uuid.UUID, wait time.Duration,
	kill func(*Entry), forceDepart func(*Entry)) AdmitOutcome {

	old := r.Get(id)
	if old == nil {
		return AdmitOutcome{OK: true}
	}

	out := AdmitOutcome{}
	if old.State() == Live {
		out.Evicted = true
		common.LogWarn("[evict] uuid=%s: new connection evicts live session gen=%d (%s)",
			id, old.Generation, old.Transport)
		kill(old)
	}

	select {
	case <-old.Departed():
		out.OK = true
		out.Waited = true
		return out
	case <-time.After(wait):
	}

	common.LogWarn("[admit] uuid=%s: departure of gen=%d did not complete within %v — departing directly",
		id, old.Generation, wait)
	forceDepart(old)

	select {
	case <-old.Departed():
		out.OK = true
		out.Waited = true
		return out
	case <-time.After(wait):
		common.LogError("[admit] uuid=%s: previous session (gen=%d) is wedged mid-departure — rejecting",
			id, old.Generation)
		return out
	}
}
