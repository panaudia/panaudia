package direct

import (
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/sessions"
)

// The reconciler is the convergence guarantee
// (plan/history/state-cleanup/mechanism-design.md §4): events (transport close,
// kick, stale timeout) are accelerators; a missed or lost event costs
// one sweep interval of staleness instead of permanent corruption. Each
// pass compares ground truth — the session registry filtered by
// Alive() — against all derived state, and runs the same DepartNode for
// anything dead or orphaned. In healthy operation it should act ~never:
// a non-zero reconciler-action count means an event path is broken, and
// the logs say which.

// DefaultReconcilePeriod is the pass cadence; the minimum-age guard is
// twice the period (a node mid-handshake is indistinguishable from an
// orphan, so nothing younger than two sweeps is actionable).
const DefaultReconcilePeriod = time.Second

// staleEscalationWait bounds how long the stale-node path waits for the
// funnel (Kill → owner exit → DepartNode) before departing directly.
// Variable so tests can shorten it.
var staleEscalationWait = 2 * time.Second

// StartReconciler runs reconcile passes every period until the backend
// stops (close of backend.Quit). Called by the deployment mains after
// backend construction.
func (backend *DirectBackend) StartReconciler(period time.Duration) {
	if period <= 0 {
		period = DefaultReconcilePeriod
	}
	minAge := 2 * period
	go func() {
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		// firstSeen ages orphaned derived state that has no registry
		// entry (and therefore no AdmittedAt) across passes.
		firstSeen := make(map[uuid.UUID]time.Time)
		for {
			select {
			case <-backend.Quit:
				return
			case <-ticker.C:
				backend.reconcilePass(minAge, firstSeen)
			}
		}
	}()
}

// reconcilePass is one sweep. Split out (and parameterised) for tests.
func (backend *DirectBackend) reconcilePass(minAge time.Duration, firstSeen map[uuid.UUID]time.Time) {
	now := time.Now()

	// 1. Registered sessions whose transport reports dead: the primary
	// trigger for ROC (activity-based Alive, no transport event exists)
	// and the backstop for a missed owner exit anywhere else. The
	// min-age guard keeps mid-handshake sessions untouched; racing a
	// concurrent event-driven departure is safe (the entry CAS admits
	// one winner).
	for _, e := range backend.Sessions.Snapshot() {
		if e.State() != sessions.Live {
			continue
		}
		if now.Sub(e.AdmittedAt) < minAge {
			continue
		}
		if e.Session.Alive() {
			continue
		}
		common.LogWarn("[reconciler] departing dead session uuid=%s gen=%d transport=%s",
			e.Uuid, e.Generation, e.Transport)
		backend.DepartNode(e, ReasonReconciler)
	}

	// 2. Derived state with no registry entry at all — orphans. Aged
	// across two thresholds: firstSeen (recorded one pass, actioned
	// only minAge later) so state mid-installation is never swept. The
	// synthetic entry is installed atomically iff still no real entry
	// (RegisterOrphan), then the standard departure applies.
	derived := backend.derivedUuids()
	for id := range derived {
		if backend.Sessions.Get(id) != nil {
			delete(firstSeen, id)
			continue
		}
		seen, ok := firstSeen[id]
		if !ok {
			firstSeen[id] = now
			continue
		}
		if now.Sub(seen) < minAge {
			continue
		}
		delete(firstSeen, id)
		if e := backend.Sessions.RegisterOrphan(id, "orphan"); e != nil {
			common.LogWarn("[reconciler] sweeping orphaned state uuid=%s", id)
			backend.DepartNode(e, ReasonReconciler)
		}
	}
	for id := range firstSeen {
		if !derived[id] {
			delete(firstSeen, id)
		}
	}

	// 3. Inverse check, log-only: a live aged session with no mixer
	// node indicates a stuck admission (ADD lost). Not auto-fixed.
	if backend.ISpace != nil {
		for _, e := range backend.Sessions.Snapshot() {
			if e.State() != sessions.Live || !e.Session.Alive() || now.Sub(e.AdmittedAt) < minAge {
				continue
			}
			if node, _ := backend.ISpace.GetNode(e.Uuid); node == nil {
				common.LogWarn("[reconciler] live session with no mixer node: uuid=%s gen=%d transport=%s admitted=%s ago",
					e.Uuid, e.Generation, e.Transport, now.Sub(e.AdmittedAt).Round(time.Millisecond))
			}
		}
	}
}

// derivedUuids returns every uuid appearing in any backend-derived
// store: the handler/bouncer/tracked-key maps and the cached entity
// existence markers (bare-uuid keys, latest op not a tombstone).
// space.Nodes is deliberately not consulted: the factory installs the
// maps before the node ADD and DepartNode enqueues the DELETE before
// releasing them, so space-only state cannot outlive the maps except
// transiently within a tick.
func (backend *DirectBackend) derivedUuids() map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool)

	backend.Lock()
	for id := range backend.HandlersByUuid {
		out[id] = true
	}
	for id := range backend.BouncersByUuid {
		out[id] = true
	}
	for id := range backend.ConnKeysBySubject {
		out[id] = true
	}
	backend.Unlock()

	if backend.Cache != nil {
		type latest struct {
			opID      uint64
			tombstone bool
		}
		markers := make(map[uuid.UUID]latest)
		for _, op := range backend.Cache.Snapshot() {
			if op.Topic != "entity" {
				continue
			}
			id, err := uuid.Parse(op.Key)
			if err != nil {
				continue // not a bare existence marker
			}
			if ex, ok := markers[id]; !ok || op.OpID > ex.opID {
				markers[id] = latest{opID: op.OpID, tombstone: op.Tombstone}
			}
		}
		for id, m := range markers {
			if !m.tombstone {
				out[id] = true
			}
		}
	}
	return out
}

// NotifyStaleNode implements space.StaleNodeNotifier: the mixer tick
// reports a node idle past TimeoutTicks. Under the funnel this is a
// Kill cause, not a removal: sever the transport and let the owner
// goroutine run the departure. If the funnel doesn't complete within
// staleEscalationWait (no owner exists — e.g. legacy-admitted sessions —
// or it is wedged), depart directly; the CAS makes the race safe.
// Called on the mixer goroutine every tick while the node stays stale,
// so the escalation is single-flight per session generation.
func (backend *DirectBackend) NotifyStaleNode(nodeUUID uuid.UUID) {
	e := backend.Sessions.Get(nodeUUID)
	if e == nil || e.State() != sessions.Live {
		return
	}

	backend.Lock()
	if backend.staleKills == nil {
		backend.staleKills = make(map[uint64]bool)
	}
	if backend.staleKills[e.Generation] {
		backend.Unlock()
		return
	}
	backend.staleKills[e.Generation] = true
	backend.Unlock()

	go func() {
		common.LogInfo("[stale] killing idle session uuid=%s gen=%d transport=%s",
			nodeUUID, e.Generation, e.Transport)
		e.Session.Kill(string(ReasonTimeout))
		select {
		case <-e.Departed():
		case <-time.After(staleEscalationWait):
			common.LogWarn("[stale] funnel did not complete for uuid=%s gen=%d — departing directly",
				nodeUUID, e.Generation)
			backend.DepartNode(e, ReasonTimeout)
		}
		backend.Lock()
		delete(backend.staleKills, e.Generation)
		backend.Unlock()
	}()
}
