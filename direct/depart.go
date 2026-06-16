package direct

import (
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/core/statecache"
)

// DepartReason and the Reason* constants moved to core/sessions in
// phase 6 so cloud-mixer backends share the vocabulary; aliased here so
// existing spatial code reads unchanged.
type DepartReason = sessions.DepartReason

const (
	ReasonTransportClosed = sessions.ReasonTransportClosed
	ReasonTimeout         = sessions.ReasonTimeout
	ReasonKicked          = sessions.ReasonKicked
	ReasonEvicted         = sessions.ReasonEvicted
	ReasonReconciler      = sessions.ReasonReconciler
	ReasonShutdown        = sessions.ReasonShutdown
	ReasonStopped         = sessions.ReasonStopped
)

// DepartNode is the single idempotent departure routine
// (plan/history/state-cleanup/mechanism-design.md §3): every removal path —
// owner-goroutine exit, kick, timeout backstop, and later the
// reconciler, eviction and shutdown — converges here, and the entry's
// Live→Departing CAS admits exactly one execution per session instance.
// The guard is per generation, not per uuid: a stale call for an old
// session provably cannot touch a successor.
//
// Ordered phases:
//  1. sever the transport (Kill — idempotent, no-op when the trigger
//     was the transport closing);
//  2. sever the inbound forwarding path (Quiesce + Bouncer.Stop) so
//     nothing of this session's can be enqueued after the announce;
//  3. announce — per-topic tombstone batches + NodeInfo3{Gone:1},
//     enqueued on the backend's standard input channels, FIFO-last;
//  4. enqueue NODE_CHANGE_DELETE (any successor's ADD lands after it);
//  5. release backend resources; the handler is handed to the mixer
//     goroutine via pendingFree — encoder cgo frees never happen here.
func (backend *DirectBackend) DepartNode(e *sessions.Entry, reason DepartReason) {
	if e == nil || !e.TryBeginDeparture() {
		return
	}
	nodeUUID := e.Uuid
	common.LogInfo("[depart] uuid=%s gen=%d transport=%s reason=%s",
		nodeUUID, e.Generation, e.Transport, reason)

	// 1. Sever the transport.
	e.Session.Kill(string(reason))

	// 2. Sever the inbound path. Identity-checked: a handler belonging
	// to a different, non-departed session is never ours to touch (a
	// successor cannot normally exist yet — admission serializes behind
	// this entry — but belt and braces). A handler whose own entry has
	// already Departed IS claimed: that's the reconciler's synthetic
	// orphan departure cleaning up state a previous departure missed.
	// ROC nodes have no ConnectionHandler; their bouncer is found via
	// the map.
	backend.Lock()
	handler := backend.HandlersByUuid[nodeUUID]
	if handler != nil && handler.registryEntry != e &&
		handler.registryEntry != nil && handler.registryEntry.State() != sessions.Departed {
		handler = nil
	}
	bouncer := backend.BouncersByUuid[nodeUUID]
	backend.Unlock()

	if handler != nil {
		handler.isActive.Store(false)
	}
	var bouncerClient *space.BouncerClient
	if bouncer != nil {
		// The bouncer's receiveSender is the per-node BouncerClient on
		// every backend flavour (direct and roc) — grab it before Stop
		// detaches it.
		bouncerClient, _ = bouncer.getReceiveSender().(*space.BouncerClient)
	}
	if bouncerClient != nil {
		bouncerClient.Quiesce()
	}
	if bouncer != nil {
		bouncer.Stop()
	}

	// 3. Announce.
	backend.announceDeparture(nodeUUID, bouncerClient)

	// 4. Remove from the mixer.
	if backend.ISpace != nil {
		backend.ISpace.DeleteNode(nodeUUID)
	}

	// 5. Release. Map deletes are identity-checked against what we
	// captured; only DepartNode removes entries from these maps
	// (single-remover rule). The handler goes to pendingFree for the
	// mixer goroutine's FreeSource to run the encoder's cgo free.
	backend.Lock()
	if handler != nil {
		if h, ok := backend.HandlersByUuid[nodeUUID]; ok && h == handler {
			delete(backend.HandlersByUuid, nodeUUID)
			// Lazy init: DirectRocBackend hand-builds the embedded
			// DirectBackend and cannot set unexported fields.
			if backend.pendingFree == nil {
				backend.pendingFree = make(map[uuid.UUID][]*ConnectionHandler)
			}
			backend.pendingFree[nodeUUID] = append(backend.pendingFree[nodeUUID], handler)
		}
	}
	if bouncer != nil {
		if b, ok := backend.BouncersByUuid[nodeUUID]; ok && b == bouncer {
			delete(backend.BouncersByUuid, nodeUUID)
		}
	}
	delete(backend.ConnKeysBySubject, nodeUUID)
	backend.Unlock()

	backend.Sessions.Unregister(e)
	e.MarkDeparted()
}

// announceDeparture emits the departure sweep for a subject: one
// tombstone batch per topic (attributes before entity — per-client
// writers route attributes through a visibility map fed by entity
// envelopes, so the entity existence tombstone must come last) plus a
// single NodeInfo3{Gone:1} on the state topic as the fast presence
// signal. Everything is enqueued on the backend's input channels with a
// zero source uuid, so it flows through the exact same dispatch path as
// live traffic — cached and enveloped on cache builds, broadcast raw on
// cacheless builds (DirectRocBackend).
//
// Key sources, unioned: the subject-tracked connection-scoped keys
// (ConnKeysBySubject — cache builds), and the BouncerClient's own
// sent-key record (the only source on cacheless builds; this is what
// AnnounceDisappearance used). Policy-scoped keys are in neither —
// they survive.
func (backend *DirectBackend) announceDeparture(nodeUUID uuid.UUID, bouncerClient *space.BouncerClient) {
	keysByTopic := make(map[string]map[string]bool)
	add := func(topic, key string) {
		keys, ok := keysByTopic[topic]
		if !ok {
			keys = make(map[string]bool)
			keysByTopic[topic] = keys
		}
		keys[key] = true
	}

	backend.Lock()
	for topic, keys := range backend.ConnKeysBySubject[nodeUUID] {
		for key := range keys {
			add(topic, key)
		}
	}
	backend.Unlock()

	if bouncerClient != nil {
		attrs, entity := bouncerClient.TakeSentKeys()
		for _, key := range attrs {
			add("attributes", key)
		}
		for _, key := range entity {
			add("entity", key)
		}
	}

	for _, topic := range sweepTopicOrder(keysByTopic) {
		keys := keysByTopic[topic]
		tombstoneOps := make([][]byte, 0, len(keys))
		for key := range keys {
			if tb, err := statecache.BuildTombstoneOp(key); err == nil {
				tombstoneOps = append(tombstoneOps, tb)
			}
		}
		var batch []byte
		if len(tombstoneOps) == 1 {
			batch = tombstoneOps[0]
		} else if len(tombstoneOps) > 1 {
			batch, _ = statecache.BuildBatch(tombstoneOps)
		}
		if len(batch) == 0 {
			continue
		}
		backend.StringChIn <- StringMessage{topic: topic, msg: string(batch)}
	}

	gone := common.NodeInfo3{Uuid: nodeUUID, Gone: 1}
	backend.DataChIn <- DataMessage{topic: "state", msg: inout.NodeInfo3ToBytes(gone)}
}

// Shutdown drains every live session for a graceful server stop (E9,
// server-fragilities #2): Kill all transports — each owner goroutine
// runs its full announced departure — wait for the departures within
// the bound, force-complete any that didn't make it (CAS-safe), then
// stop the dispatcher. Call before process exit; the per-protocol
// listeners are the caller's to close.
func (backend *DirectBackend) Shutdown(timeout time.Duration) {
	entries := backend.Sessions.Snapshot()
	common.LogInfo("[shutdown] draining %d sessions", len(entries))

	for _, e := range entries {
		go e.Session.Kill(string(ReasonShutdown))
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	expired := false
	for _, e := range entries {
		if !expired {
			select {
			case <-e.Departed():
				continue
			case <-deadline.C:
				expired = true
			}
		}
		// Past the deadline: force-complete directly. If the CAS is
		// held by a wedged departure elsewhere, give it a short grace
		// and move on — process exit is imminent either way.
		backend.DepartNode(e, ReasonShutdown)
		select {
		case <-e.Departed():
		case <-time.After(100 * time.Millisecond):
			common.LogError("[shutdown] session uuid=%s gen=%d did not depart", e.Uuid, e.Generation)
		}
	}

	common.LogInfo("[shutdown] drain complete")
	backend.Stop()
}

// sweepTopicOrder returns the departure-sweep emission order:
// attributes, then entity, then anything else.
func sweepTopicOrder(keysByTopic map[string]map[string]bool) []string {
	order := make([]string, 0, len(keysByTopic))
	if _, ok := keysByTopic["attributes"]; ok {
		order = append(order, "attributes")
	}
	if _, ok := keysByTopic["entity"]; ok {
		order = append(order, "entity")
	}
	for topic := range keysByTopic {
		if topic != "attributes" && topic != "entity" {
			order = append(order, topic)
		}
	}
	return order
}
