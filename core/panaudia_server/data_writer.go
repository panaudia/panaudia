package panaudia_server

import (
	"encoding/json"
	"strings"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/commands"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/statecache"
	"github.com/pion/webrtc/v3"
)

// pendingMaxBytes caps the per-channel pre-open buffer. 1 MiB is
// large enough for any realistic backfill (typical: a few KB) and
// small enough to be a safe upper bound if a channel never opens.
const pendingMaxBytes = 1 * 1024 * 1024

type DataWriter struct {
	// dcMu guards the *DataChannel pointers and their pending buffers.
	// The OnOpen callbacks (called on a pion-internal goroutine) and
	// the SendString path (called on the bouncer's broadcast goroutine)
	// both touch these fields, so the assignments and flushes need to
	// be serialised.
	dcMu              sync.Mutex
	StateDataChannel  *webrtc.DataChannel
	SourceDataChannel *webrtc.DataChannel
	EntityDataChannel *webrtc.DataChannel
	// SpaceDataChannel carries the space-wide topic (roles-muted /
	// roles-kicked / roles-gain / roles-attenuation). Always created
	// during addDataChannels regardless of caps — gated at the
	// sendSpace path on the spaceRead flag, see
	// plan/history/commands/space-read-path-plan.md key design decision 5.
	SpaceDataChannel *webrtc.DataChannel

	// Pending pre-open buffers. SetReceiveSender triggers the cache
	// backfill the moment a session connects, but the WebRTC data
	// channels aren't open until the SCTP handshake completes a few
	// hundred ms later. Without buffering, every backfilled envelope
	// that hits Send during that window would be silently dropped.
	// Each AppendPending call is bounded by pendingMaxBytes — beyond
	// that we drop the oldest entries (FIFO) to keep memory bounded
	// if a channel never opens.
	pendingAttrs       [][]byte
	pendingAttrsBytes  int
	pendingEntity      [][]byte
	pendingEntityBytes int
	pendingSpace       [][]byte
	pendingSpaceBytes  int

	DeadSessionCh chan uint64
	// MyNodeID is the uuid of the connection this writer serves. Used by
	// the per-client entity filter — only entity ops whose key is exactly
	// `{myNodeID}` or starts with `{myNodeID}.` are forwarded.
	MyNodeID    uuid.UUID
	myNodeIDStr string
	// entityReadAll, when true, bypasses the per-client entity-key
	// filter so the holder sees every node's entity ops. Resolved at
	// authentication time from the holder's roles via the
	// commands.ReadCapEntityAll cap.
	entityReadAll bool
	// spaceRead, when true, allows space-topic envelopes through to
	// the holder's space data channel. Resolved from
	// commands.ReadCapSpaceRead at auth time. Without it sendSpace
	// drops every envelope, leaving SpaceDataChannel idle.
	spaceRead bool
	// SubSpaces is this gateway's own subspace set, sourced from the
	// connection's NodeConfig at construction time. An empty set means
	// "see everything".
	SubSpaces mapset.Set[uuid.UUID]
	// Members tracks uuids whose attributes we have forwarded — used by
	// SendData to filter state messages by visibility.
	Members mapset.Set[uuid.UUID]
	// nodeSubspaces holds each remote node's subspace set, learnt from
	// the cached "entity" topic. Used to apply the overlap rule when
	// forwarding attributes / state to this connection's client.
	nodeSubspaces map[uuid.UUID]mapset.Set[uuid.UUID]
}

// NewDataWriter creates a per-WebRTC-connection writer. readCaps is the
// resolved set of read scopes for the holder (see
// common.NodeConfig.ReadCaps). nil/empty selects the default per-client
// entity filter and drops space envelopes.
func NewDataWriter(myNodeID uuid.UUID, SubSpaces []uuid.UUID, readCaps map[string]bool) *DataWriter {
	writer := DataWriter{}
	writer.MyNodeID = myNodeID
	writer.myNodeIDStr = myNodeID.String()
	writer.entityReadAll = readCaps[commands.ReadCapEntityAll]
	writer.spaceRead = readCaps[commands.ReadCapSpaceRead]
	writer.SubSpaces = mapset.NewSet[uuid.UUID]()
	writer.Members = mapset.NewSet[uuid.UUID]()
	writer.nodeSubspaces = make(map[uuid.UUID]mapset.Set[uuid.UUID])
	for _, subSpace := range SubSpaces {
		writer.SubSpaces.Add(subSpace)
	}
	return &writer
}

func (writer *DataWriter) SendString(eventName string, jsonString string) {
	switch eventName {
	case "entity":
		// Update our per-node subspace map AND forward the (filtered)
		// envelope to the client over the entity data channel. The
		// nil-check on EntityDataChannel happens inside sendEntity so
		// envelopes arriving before OnOpen get buffered rather than
		// dropped (see the per-channel pending buffer in DataWriter).
		payload := []byte(jsonString)
		writer.handleEntity(payload)
		writer.sendEntity(payload)
		return
	case "attributes":
		// The nil-check on SourceDataChannel happens inside
		// sendAttributes for the same reason.
		writer.sendAttributes([]byte(jsonString))
		return
	case "space":
		// Read-cap gated forward to the space data channel; the
		// channel is always created (see addDataChannels) but the
		// holder only receives ops if granted commands.ReadCapSpaceRead.
		writer.sendSpace([]byte(jsonString))
		return
	}

	if writer.SourceDataChannel == nil {
		return
	}
	sendErr := writer.SourceDataChannel.SendText(jsonString)
	if sendErr != nil {
		common.LogInfo("DataWriter SendString failed but dont worry: %v", sendErr)
	}
}

// sendAttributes forwards an attribute message to the WebRTC client,
// applying the subspace overlap rule using the per-node subspace map
// learnt from the "entity" topic.
//
// Cache-envelope path (current production): the payload starts with the
// envelope version byte (0xCA). Decode it, walk the inner per-key ops,
// pick out the source uuid, and forward iff the source's subspaces
// overlap this gateway's subspaces (or this gateway has no subspaces).
//
// Legacy plain-JSON path: a monolithic `{uuid, subspaces, ...}` blob.
// Kept for backward compatibility with any non-refactored sender.
func (writer *DataWriter) sendAttributes(payload []byte) {
	if statecache.IsCacheEnvelope(payload) {
		envelope, err := statecache.Decode(payload)
		if err != nil {
			common.LogError("DataWriter: failed to decode cache envelope: %v", err)
			return
		}

		ops, _, err := statecache.ParseOps(envelope.Value)
		if err != nil {
			common.LogWarn("DataWriter: failed to parse cache envelope ops: %v", err)
			return
		}

		// Identify the source uuid from the first op (a single envelope
		// always carries one node's keys — see BouncerClient.sendAttributes).
		var sourceID uuid.UUID
		ok := false
		for _, op := range ops {
			if id, isUUID := uuidFromOpKey(op.Key); isUUID {
				sourceID = id
				ok = true
				break
			}
		}
		if !ok {
			return
		}
		if !writer.uuidVisible(sourceID) {
			return
		}
		writer.Members.Add(sourceID)

		writer.sendOrBufferAttrs(payload)
		return
	}

	// Legacy plain-JSON path.
	attributes := common.J{}
	if err := json.Unmarshal(payload, &attributes); err != nil {
		common.LogError("Error in Unmarshal jsonString: %v", err)
		return
	}

	if attributes["subspaces"] == nil {
		if !writer.SubSpaces.IsEmpty() {
			return
		}
	} else {
		subSpaces := mapset.NewSet[uuid.UUID]()
		sss := attributes["subspaces"].([]interface{})

		for _, ss := range sss {
			subSpaces.Add(uuid.MustParse(ss.(string)))
		}

		if !writer.SubSpaces.IsEmpty() || !subSpaces.IsEmpty() {
			if !writer.SubSpaces.ContainsAnyElement(subSpaces) {
				return
			}
		}
	}

	memberID, err := uuid.Parse(attributes["uuid"].(string))
	if err != nil {
		return
	}
	writer.Members.Add(memberID)

	writer.sendOrBufferAttrs(payload)
}

// sendEntity forwards an entity envelope to the client's entity data
// channel, retaining only the ops whose leading uuid matches this
// writer's own node id (i.e. `key == myID || strings.HasPrefix(key, myID+".")`).
//
// If no ops survive the filter the envelope is dropped. If exactly one
// op survives the envelope is rebuilt around that single op; otherwise
// a batch envelope is built from the survivors. The OpID, topic and HLC
// of the original envelope are preserved so the client's resume cursor
// advances even across envelopes that filter out entirely.
func (writer *DataWriter) sendEntity(payload []byte) {
	if !statecache.IsCacheEnvelope(payload) {
		return
	}
	envelope, err := statecache.Decode(payload)
	if err != nil {
		common.LogError("DataWriter: failed to decode entity envelope: %v", err)
		return
	}
	_, rawOps, err := statecache.ParseOps(envelope.Value)
	if err != nil {
		common.LogWarn("DataWriter: failed to parse entity ops for forwarding: %v", err)
		return
	}

	kept := make([][]byte, 0, len(rawOps))
	for _, raw := range rawOps {
		var op statecache.JsonOp
		if jerr := json.Unmarshal(raw, &op); jerr != nil {
			continue
		}
		if !writer.entityKeyVisible(op.Key) {
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
		inner, err = statecache.BuildBatch(kept)
		if err != nil {
			common.LogWarn("DataWriter: failed to rebuild entity batch: %v", err)
			return
		}
	}

	rebuilt := envelope
	rebuilt.Value = inner
	out, err := statecache.Encode(rebuilt)
	if err != nil {
		common.LogError("DataWriter: failed to re-encode entity envelope: %v", err)
		return
	}

	writer.sendOrBufferEntity(out)
}

// sendOrBufferAttrs forwards `payload` to the attributes data channel
// if it's open; otherwise queues it in pendingAttrs until OnOpen fires.
// Caller has already applied subspace / membership filters.
func (writer *DataWriter) sendOrBufferAttrs(payload []byte) {
	writer.dcMu.Lock()
	ch := writer.SourceDataChannel
	if ch == nil {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		writer.pendingAttrs = trimPending(writer.pendingAttrs, &writer.pendingAttrsBytes, cp)
		writer.dcMu.Unlock()
		return
	}
	writer.dcMu.Unlock()

	if sendErr := ch.Send(payload); sendErr != nil {
		common.LogInfo("DataWriter SourceDataChannel.Send failed but dont worry: %v", sendErr)
	}
}

// sendSpace forwards a space-topic envelope to the client's space data
// channel. The single visibility check is the spaceRead cap (resolved
// from commands.ReadCapSpaceRead at auth time); space envelopes have
// no uuid prefix so there is no per-key filter — the topic is
// space-wide. When the cap isn't granted we drop pre-buffer too, so
// the pending queue only accumulates for authorised holders.
func (writer *DataWriter) sendSpace(payload []byte) {
	if !writer.spaceRead {
		return
	}
	writer.sendOrBufferSpace(payload)
}

// sendOrBufferSpace is the space-channel equivalent of sendOrBufferAttrs.
func (writer *DataWriter) sendOrBufferSpace(payload []byte) {
	writer.dcMu.Lock()
	ch := writer.SpaceDataChannel
	if ch == nil {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		writer.pendingSpace = trimPending(writer.pendingSpace, &writer.pendingSpaceBytes, cp)
		writer.dcMu.Unlock()
		return
	}
	writer.dcMu.Unlock()

	if sendErr := ch.Send(payload); sendErr != nil {
		common.LogInfo("DataWriter SpaceDataChannel.Send failed but dont worry: %v", sendErr)
	}
}

// sendOrBufferEntity is the entity-channel equivalent of sendOrBufferAttrs.
func (writer *DataWriter) sendOrBufferEntity(payload []byte) {
	writer.dcMu.Lock()
	ch := writer.EntityDataChannel
	if ch == nil {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		writer.pendingEntity = trimPending(writer.pendingEntity, &writer.pendingEntityBytes, cp)
		writer.dcMu.Unlock()
		return
	}
	writer.dcMu.Unlock()

	if sendErr := ch.Send(payload); sendErr != nil {
		common.LogInfo("DataWriter EntityDataChannel.Send failed but dont worry: %v", sendErr)
	}
}

// AttachAttributesChannel installs the attributes data channel and
// flushes any envelopes that arrived before it was open. Called from
// the channel's OnOpen handler.
func (writer *DataWriter) AttachAttributesChannel(d *webrtc.DataChannel) {
	writer.dcMu.Lock()
	writer.SourceDataChannel = d
	pending := writer.pendingAttrs
	writer.pendingAttrs = nil
	writer.pendingAttrsBytes = 0
	writer.dcMu.Unlock()

	for _, msg := range pending {
		if err := d.Send(msg); err != nil {
			common.LogInfo("DataWriter: failed to drain pending attribute envelope: %v", err)
		}
	}
}

// AttachEntityChannel is the entity-channel equivalent of
// AttachAttributesChannel.
func (writer *DataWriter) AttachEntityChannel(d *webrtc.DataChannel) {
	writer.dcMu.Lock()
	writer.EntityDataChannel = d
	pending := writer.pendingEntity
	writer.pendingEntity = nil
	writer.pendingEntityBytes = 0
	writer.dcMu.Unlock()

	for _, msg := range pending {
		if err := d.Send(msg); err != nil {
			common.LogInfo("DataWriter: failed to drain pending entity envelope: %v", err)
		}
	}
}

// AttachSpaceChannel is the space-channel equivalent of
// AttachAttributesChannel. The pending queue is empty for holders
// without commands.ReadCapSpaceRead — sendSpace's pre-buffer gate
// drops their envelopes — so this is effectively a no-op drain in
// the no-cap case.
func (writer *DataWriter) AttachSpaceChannel(d *webrtc.DataChannel) {
	writer.dcMu.Lock()
	writer.SpaceDataChannel = d
	pending := writer.pendingSpace
	writer.pendingSpace = nil
	writer.pendingSpaceBytes = 0
	writer.dcMu.Unlock()

	for _, msg := range pending {
		if err := d.Send(msg); err != nil {
			common.LogInfo("DataWriter: failed to drain pending space envelope: %v", err)
		}
	}
}

// trimPending appends `cp` to `pending`, dropping the oldest entries
// (FIFO) until total bytes stay below pendingMaxBytes. Called under
// dcMu.
func trimPending(pending [][]byte, totalBytes *int, cp []byte) [][]byte {
	for *totalBytes+len(cp) > pendingMaxBytes && len(pending) > 0 {
		*totalBytes -= len(pending[0])
		pending = pending[1:]
	}
	*totalBytes += len(cp)
	return append(pending, cp)
}

// entityKeyVisible returns true iff the entity op key is one this
// writer's client is permitted to see. Default rule: key is exactly
// `{myNodeID}` (existence marker / future per-uuid leaf) or starts with
// `{myNodeID}.`. The dot-suffix check prevents false positives where
// another uuid happens to share a leading byte sequence. Holders
// granted the commands.ReadCapEntityAll scope short-circuit to true —
// the cache already holds every node's entity ops globally, so the
// backfill snapshot delivers them with no cache change required.
func (writer *DataWriter) entityKeyVisible(key string) bool {
	if writer.entityReadAll {
		return true
	}
	id := writer.myNodeIDStr
	if key == id {
		return true
	}
	if len(key) > len(id) && key[len(id)] == '.' && key[:len(id)] == id {
		return true
	}
	return false
}

// handleEntity decodes an incoming "entity" cache envelope and updates
// the gateway's per-node visibility map. Entity messages drive the
// internal subspace filter applied to attributes/state forwarding; the
// per-client read path forwards the (filtered) entity envelope to the
// client separately in sendEntity().
//
// Flat-key schema:
//   - `{uuid}`                              — existence marker (set/tombstone)
//   - `{uuid}.subspaces.{ss_id}`            — one op per subspace this node is in
//   - other `{uuid}.{field}` keys           — ignored here
func (writer *DataWriter) handleEntity(payload []byte) {
	if !statecache.IsCacheEnvelope(payload) {
		return
	}
	envelope, err := statecache.Decode(payload)
	if err != nil {
		common.LogError("DataWriter: failed to decode entity envelope: %v", err)
		return
	}
	ops, _, err := statecache.ParseOps(envelope.Value)
	if err != nil {
		common.LogWarn("DataWriter: failed to parse entity ops: %v", err)
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
				delete(writer.nodeSubspaces, nodeID)
				writer.Members.Remove(nodeID)
			} else {
				writer.Members.Add(nodeID)
			}
		case strings.HasPrefix(rest, "subspaces."):
			ssStr := rest[len("subspaces."):]
			ssID, perr := uuid.Parse(ssStr)
			if perr != nil {
				continue
			}
			set, exists := writer.nodeSubspaces[nodeID]
			if op.Tombstone {
				if exists {
					set.Remove(ssID)
				}
				continue
			}
			if !exists {
				set = mapset.NewSet[uuid.UUID]()
				writer.nodeSubspaces[nodeID] = set
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

// uuidVisible answers "should this gateway forward node N's traffic to
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
func (writer *DataWriter) uuidVisible(node uuid.UUID) bool {
	peerSet, _ := writer.nodeSubspaces[node] // nil if unknown
	peerEmpty := peerSet == nil || peerSet.IsEmpty()
	writerEmpty := writer.SubSpaces.IsEmpty()

	if writerEmpty && peerEmpty {
		return true
	}
	if writerEmpty || peerEmpty {
		return false
	}
	return writer.SubSpaces.ContainsAnyElement(peerSet)
}

// uuidFromOpKey extracts the uuid from a per-key op path. The convention
// is that the first dotted segment is always the entity uuid.
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

func (writer *DataWriter) SendData(eventName string, data []byte) {
	if eventName == "state" {
		if writer.StateDataChannel != nil {

			info := inout.NodeInfo3FromBytes(data)

			if !writer.Members.Contains(info.Uuid) {
				return
			}

			sendErr := writer.StateDataChannel.Send(data)
			if sendErr != nil {
				//fmt.Printf("DataWriter SendData failed but dont worry: %v", sendErr)
				//panic(sendErr)
			}
		}
	}
	if eventName == "session" {
		sessionid := inout.DecodeUint64(data)
		if writer.DeadSessionCh != nil {
			common.LogInfo("Dead session: %d", sessionid)
			writer.DeadSessionCh <- sessionid
		} else {
			common.LogInfo("Dead session but no channel: %d", sessionid)
		}
	}
}
