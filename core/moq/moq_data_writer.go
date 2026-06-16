package moq

import (
	"encoding/json"
	"strings"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/commands"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/statecache"
	"github.com/pion/webrtc/v3/pkg/media"
)

// entitySink is the minimal interface MoqDataWriter needs to deliver a
// filtered entity envelope to the client. *MoqTrackAdapter satisfies it
// in production; tests substitute a capturing fake.
type entitySink interface {
	WriteSample(sample media.Sample) error
}

// MoqDataWriter implements space.IMessageSender for MOQ sessions.
// It bridges the backend's state/attributes/entity broadcasts to MOQ
// output tracks. State and attributes are filtered by subspace
// membership; entity is filtered to only the keys whose leading uuid
// matches this session's own node id (per-client read).
type MoqDataWriter struct {
	stateAdapter      *MoqTrackAdapter
	attributesAdapter *MoqTrackAdapter
	entityAdapter     entitySink
	// spaceAdapter is nil for connections that don't hold
	// commands.ReadCapSpaceRead. The space output track is never
	// announced for those holders (see SubscriptionManager.Start), so
	// the adapter wouldn't have any subscribers anyway, but we also
	// gate the send path on spaceRead to make the contract explicit.
	spaceAdapter entitySink
	myNodeID     uuid.UUID
	myNodeIDStr  string
	subSpaces    mapset.Set[uuid.UUID]
	members      mapset.Set[uuid.UUID]
	// entityReadAll, when true, bypasses the per-client entity-key
	// filter so the holder sees every node's entity ops, not just keys
	// prefixed with their own uuid. Resolved at authentication time
	// from the holder's roles via the commands.ReadCapEntityAll cap.
	entityReadAll bool
	// spaceRead, when true, allows space-topic envelopes through to
	// the holder. Resolved from commands.ReadCapSpaceRead at auth
	// time. Without it, sendSpace drops every envelope.
	spaceRead bool
	// nodeSubspaces holds each remote node's subspace set, learnt from
	// the cached "entity" topic. Used to apply the overlap rule when
	// forwarding attributes / state to this MOQ session's client.
	nodeSubspaces map[uuid.UUID]mapset.Set[uuid.UUID]
}

// NewMoqDataWriter creates a data writer that publishes state, attributes
// and per-client filtered entity ops to the given MOQ track adapters,
// plus the space-wide topic to holders with the relevant read cap.
// readCaps is the resolved set of read scopes for the holder (see
// common.NodeConfig.ReadCaps). nil/empty selects the default
// per-client entity filter and drops space envelopes.
func NewMoqDataWriter(stateAdapter, attributesAdapter, entityAdapter, spaceAdapter *MoqTrackAdapter, myNodeID uuid.UUID, subSpaces []uuid.UUID, readCaps map[string]bool) *MoqDataWriter {
	w := &MoqDataWriter{
		stateAdapter:      stateAdapter,
		attributesAdapter: attributesAdapter,
		myNodeID:          myNodeID,
		myNodeIDStr:       myNodeID.String(),
		subSpaces:         mapset.NewSet[uuid.UUID](),
		members:           mapset.NewSet[uuid.UUID](),
		entityReadAll:     readCaps[commands.ReadCapEntityAll],
		spaceRead:         readCaps[commands.ReadCapSpaceRead],
		nodeSubspaces:     make(map[uuid.UUID]mapset.Set[uuid.UUID]),
	}
	// Avoid wrapping a nil *MoqTrackAdapter in a non-nil interface value —
	// the existing `entityAdapter == nil` check only works when the
	// interface itself is nil. Same for spaceAdapter.
	if entityAdapter != nil {
		w.entityAdapter = entityAdapter
	}
	if spaceAdapter != nil {
		w.spaceAdapter = spaceAdapter
	}
	for _, ss := range subSpaces {
		w.subSpaces.Add(ss)
	}
	return w
}

// SendString implements space.IMessageSender. Routes:
//   - "attributes" → subspace-filtered forward to attributes output track
//   - "entity"     → update internal visibility map AND per-uuid filtered
//     forward to entity output track (the client only sees
//     keys prefixed with its own uuid).
//   - "space"      → forward verbatim to space output track if the
//     holder has the commands.ReadCapSpaceRead cap;
//     drop otherwise. Keys are uuid-less so no per-key
//     filter is applied.
func (w *MoqDataWriter) SendString(topic string, msg string) {
	switch topic {
	case "entity":
		payload := []byte(msg)
		w.handleEntity(payload)
		w.sendEntity(payload)
		return
	case "attributes":
		if w.attributesAdapter == nil {
			return
		}
		w.sendAttributes([]byte(msg))
	case "space":
		w.sendSpace([]byte(msg))
	}
}

// sendSpace forwards a space-topic envelope verbatim to the client's
// space output track. The single visibility check is the spaceRead
// cap; there is no per-key filter because the topic is space-wide
// (keys have no uuid prefix). Drops the envelope when the cap isn't
// granted or the adapter is nil — same outcome either way, but the
// adapter is also nil for unauthorised holders since the handler is
// skipped at SubscriptionManager.Start.
func (w *MoqDataWriter) sendSpace(payload []byte) {
	if !w.spaceRead || w.spaceAdapter == nil {
		return
	}
	if err := w.spaceAdapter.WriteSample(media.Sample{Data: payload}); err != nil {
		common.LogError("MoqDataWriter: failed to send space: %v", err)
	}
}

// sendEntity forwards an entity envelope to the client's entity output
// track, retaining only the ops whose leading uuid matches this writer's
// own node id (i.e. `key == myID || strings.HasPrefix(key, myID+".")`).
//
// If the envelope is empty after filtering it is dropped. If exactly one
// op remains the envelope is rebuilt from that single op; otherwise a
// batch envelope is rebuilt. The OpID, topic and HLC of the original
// envelope are preserved so the client's resume cursor advances even
// across envelopes that filter out entirely.
func (w *MoqDataWriter) sendEntity(payload []byte) {
	if w.entityAdapter == nil {
		return
	}
	if !statecache.IsCacheEnvelope(payload) {
		return
	}
	envelope, err := statecache.Decode(payload)
	if err != nil {
		common.LogError("MoqDataWriter: failed to decode entity envelope: %v", err)
		return
	}
	_, rawOps, err := statecache.ParseOps(envelope.Value)
	if err != nil {
		common.LogWarn("MoqDataWriter: failed to parse entity ops for forwarding: %v", err)
		return
	}

	kept := make([]json.RawMessage, 0, len(rawOps))
	for _, raw := range rawOps {
		var op statecache.JsonOp
		if jerr := json.Unmarshal(raw, &op); jerr != nil {
			continue
		}
		if !w.entityKeyVisible(op.Key) {
			continue
		}
		kept = append(kept, raw)
	}
	if len(kept) == 0 {
		return
	}

	var inner []byte
	if len(kept) == 1 {
		inner = kept[0]
	} else {
		raws := make([][]byte, len(kept))
		for i, r := range kept {
			raws[i] = r
		}
		inner, err = statecache.BuildBatch(raws)
		if err != nil {
			common.LogWarn("MoqDataWriter: failed to rebuild entity batch: %v", err)
			return
		}
	}

	rebuilt := envelope
	rebuilt.Value = inner
	out, err := statecache.Encode(rebuilt)
	if err != nil {
		common.LogError("MoqDataWriter: failed to re-encode entity envelope: %v", err)
		return
	}

	if err := w.entityAdapter.WriteSample(media.Sample{Data: out}); err != nil {
		common.LogError("MoqDataWriter: failed to send entity: %v", err)
	}
}

// entityKeyVisible returns true iff the entity op key is one this client
// is permitted to see. Default rule: key is exactly the client's uuid
// (existence marker / future per-uuid leaf) or starts with `{myUuid}.`.
// The dot-suffix check prevents false positives where another uuid
// happens to share a leading byte sequence. Holders granted the
// commands.ReadCapEntityAll scope short-circuit to true — the cache
// already holds every node's entity ops globally, so the backfill
// snapshot delivers them with no cache change required.
func (w *MoqDataWriter) entityKeyVisible(key string) bool {
	if w.entityReadAll {
		return true
	}
	id := w.myNodeIDStr
	if key == id {
		return true
	}
	if len(key) > len(id) && key[len(id)] == '.' && key[:len(id)] == id {
		return true
	}
	return false
}

// sendAttributes applies the subspace overlap filter and forwards the
// envelope to the MOQ attributes output track. The envelope is sent
// verbatim; the client decodes it.
func (w *MoqDataWriter) sendAttributes(payload []byte) {
	if statecache.IsCacheEnvelope(payload) {
		envelope, err := statecache.Decode(payload)
		if err != nil {
			common.LogError("MoqDataWriter: failed to decode cache envelope: %v", err)
			return
		}
		ops, _, err := statecache.ParseOps(envelope.Value)
		if err != nil {
			common.LogWarn("MoqDataWriter: failed to parse envelope ops: %v", err)
			return
		}
		var sourceID uuid.UUID
		ok := false
		for _, op := range ops {
			if id, isUUID := uuidFromOpKey(op.Key); isUUID {
				sourceID = id
				ok = true
				break
			}
		}
		if !ok || !w.uuidVisible(sourceID) {
			return
		}
		w.members.Add(sourceID)

		if err := w.attributesAdapter.WriteSample(media.Sample{Data: payload}); err != nil {
			common.LogError("MoqDataWriter: failed to send attributes: %v", err)
		}
		return
	}

	// Legacy plain-JSON path.
	attributes := common.J{}
	if err := json.Unmarshal(payload, &attributes); err != nil {
		common.LogError("MoqDataWriter: error unmarshalling attributes: %v", err)
		return
	}

	if attributes["subspaces"] == nil {
		if !w.subSpaces.IsEmpty() {
			return
		}
	} else {
		senderSubSpaces := mapset.NewSet[uuid.UUID]()
		sss := attributes["subspaces"].([]interface{})
		for _, ss := range sss {
			senderSubSpaces.Add(uuid.MustParse(ss.(string)))
		}
		if !w.subSpaces.IsEmpty() || !senderSubSpaces.IsEmpty() {
			if !w.subSpaces.ContainsAnyElement(senderSubSpaces) {
				return
			}
		}
	}

	memberID, err := uuid.Parse(attributes["uuid"].(string))
	if err != nil {
		return
	}
	w.members.Add(memberID)

	if err := w.attributesAdapter.WriteSample(media.Sample{Data: payload}); err != nil {
		common.LogError("MoqDataWriter: failed to send attributes: %v", err)
	}
}

// handleEntity updates this writer's per-node subspace map from an
// incoming entity envelope. Entity messages drive the gateway's internal
// visibility state (members + nodeSubspaces) but the forwarding to the
// connected client happens separately in sendEntity().
//
// Flat-key schema:
//   - `{uuid}`                              — existence marker (set/tombstone)
//   - `{uuid}.subspaces.{ss_id}`            — one op per subspace this node is in
//   - other `{uuid}.{field}` keys           — ignored here; future visibility
//     concerns will read them as needed
func (w *MoqDataWriter) handleEntity(payload []byte) {
	if !statecache.IsCacheEnvelope(payload) {
		return
	}
	envelope, err := statecache.Decode(payload)
	if err != nil {
		common.LogError("MoqDataWriter: failed to decode entity envelope: %v", err)
		return
	}
	ops, _, err := statecache.ParseOps(envelope.Value)
	if err != nil {
		common.LogWarn("MoqDataWriter: failed to parse entity ops: %v", err)
		return
	}
	for _, op := range ops {
		nodeID, rest, ok := splitEntityKey(op.Key)
		if !ok {
			continue
		}
		switch {
		case rest == "":
			// Existence marker.
			if op.Tombstone {
				delete(w.nodeSubspaces, nodeID)
				w.members.Remove(nodeID)
			} else {
				w.members.Add(nodeID)
			}
		case strings.HasPrefix(rest, "subspaces."):
			ssStr := rest[len("subspaces."):]
			ssID, perr := uuid.Parse(ssStr)
			if perr != nil {
				continue
			}
			set, exists := w.nodeSubspaces[nodeID]
			if op.Tombstone {
				if exists {
					set.Remove(ssID)
				}
				continue
			}
			if !exists {
				set = mapset.NewSet[uuid.UUID]()
				w.nodeSubspaces[nodeID] = set
			}
			set.Add(ssID)
		}
	}
}

// splitEntityKey separates the leading uuid from the remaining dotted
// path of an entity flat key. Returns (uuid, "", true) for a bare
// `{uuid}` marker key, (uuid, "field", true) for `{uuid}.field`,
// (uuid, "field.x", true) for `{uuid}.field.x`, and (_, _, false) if
// the leading segment is not a valid uuid.
func splitEntityKey(key string) (uuid.UUID, string, bool) {
	dot := strings.IndexByte(key, '.')
	var head, rest string
	if dot < 0 {
		head = key
	} else {
		head = key[:dot]
		rest = key[dot+1:]
	}
	id, err := uuid.Parse(head)
	if err != nil {
		return uuid.UUID{}, "", false
	}
	return id, rest, true
}

// uuidVisible answers "should this writer forward node N's traffic to
// its connected client?" using the project-wide subspace rule:
//
//   - if you have no subspaces, you only see others with no subspaces
//   - if you have any subspaces, you only see others sharing one
//
// In other words: subspace membership partitions visibility — the "open
// space" (no subspaces) is its own partition, separate from any named
// subspace. A node we have no entity record for is treated as having no
// subspaces.
//
// This matches the audio encoder's `shouldIncludePeer` rule
// (core/ambisonic/encoder.go) so audio and metadata visibility agree.
//
// Ordering invariant: callers (sendAttributes) rely on the source's
// entity envelope having already been processed by handleEntity so
// nodeSubspaces[node] is populated. Both the live path
// (core/space/bouncer_client.go's per-tick loop) and the backfill path
// (direct/backend.go BackfillBouncer) order entity-before-attributes to
// uphold this. Without the ordering, this function returns false for
// any writer-with-subspaces and the attribute is silently dropped. See
// plan/history/distributed-state-sync/topic-ordering.md.
func (w *MoqDataWriter) uuidVisible(node uuid.UUID) bool {
	peerSet, _ := w.nodeSubspaces[node] // nil if unknown
	peerEmpty := peerSet == nil || peerSet.IsEmpty()
	writerEmpty := w.subSpaces.IsEmpty()

	if writerEmpty && peerEmpty {
		return true
	}
	if writerEmpty || peerEmpty {
		return false
	}
	return w.subSpaces.ContainsAnyElement(peerSet)
}

// uuidFromOpKey extracts the uuid from a per-key op path. The first
// dotted segment is always the entity uuid.
func uuidFromOpKey(key string) (uuid.UUID, bool) {
	dot := strings.IndexByte(key, '.')
	var head string
	if dot < 0 {
		head = key
	} else {
		head = key[:dot]
	}
	id, err := uuid.Parse(head)
	if err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

// SendData implements space.IMessageSender.
// Routes "state" topic (48-byte NodeInfo3) to the state output adapter,
// only for nodes that passed the subspace filter in SendString.
func (w *MoqDataWriter) SendData(topic string, data []byte) {
	if topic == "state" && w.stateAdapter != nil {

		info := inout.NodeInfo3FromBytes(data)
		if !w.members.Contains(info.Uuid) {
			return
		}

		if err := w.stateAdapter.WriteSample(media.Sample{
			Data: data,
		}); err != nil {
			common.LogError("MoqDataWriter: failed to send state: %v", err)
		}
	}
}
