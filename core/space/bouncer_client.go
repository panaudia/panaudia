package space

import (
	"encoding/json"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/commands"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/statecache"
	"github.com/panaudia/panaudia/core/timing"
)

type ILocationUpdater interface {
	UpdateLocation(Uuid uuid.UUID, position common.Position, rotation common.Rotation)
}

// pendingMaxBytes caps the per-client pre-attach buffer. The window
// between NewBouncerClient (registers our bouncer with the backend) and
// the eventual SetReceiveSender (wires the writer) is normally
// microseconds, but the dispatcher's broadcast goroutine can land
// messages on our bouncer.StringChOut during that gap. 1 MiB is more
// than enough for any realistic burst and small enough to bound memory
// if SetReceiveSender never lands.
const pendingMaxBytes = 1 * 1024 * 1024

type pendingString struct {
	topic string
	msg   string
}

type pendingData struct {
	topic string
	data  []byte
}

type BouncerClient struct {
	nodeConfig      common.NodeConfig
	bouncer         IMessageSenderReceiver
	position        common.Position
	rotation        common.Rotation
	volumeCounter   int
	volumeSum       float64
	volume          float64
	done            atomic.Bool
	gone            int32
	LocationUpdater ILocationUpdater
	sentKeys        map[string]bool // tracks attribute keys we've sent, for tombstoning on disconnect
	sentEntityKeys  map[string]bool // tracks entity keys we've sent, for tombstoning on disconnect
	mu              sync.Mutex

	// Receive-side state, guarded by rsMu.
	//
	// `receiveSender` is the writer that delivers cached/live ops to
	// this connection's transport (MoqDataWriter / DataWriter). It is
	// nil between NewBouncerClient (registers our bouncer with the
	// backend) and SetReceiveSender (wires the writer). The dispatcher
	// can broadcast to our bouncer.StringChOut during that micro-window;
	// without buffering those messages they would be dropped here and
	// the connecting client would not see the corresponding peer until
	// that peer's next periodic re-emit (~4 s).
	//
	// `attached` flips true the first time SetReceiveSender is called.
	// We only buffer pre-attach — once a writer has been attached, a
	// later nil (Stop()) means the connection is tearing down and
	// further messages should be dropped, not held.
	rsMu           sync.Mutex
	receiveSender  IMessageSender
	attached       bool
	pendingStrings []pendingString
	pendingData    []pendingData
	pendingBytes   int

	// Command dispatch — both nil while no command handler has been
	// installed; HandleCommand silently drops invocations until the
	// backend wires these via SetCommandHandler.
	cmdRegistry   *commands.Registry
	cmdAuthorizer commands.Authorizer

	// Kick handling. Late-bound by the deployment's NewConnectionHandler
	// once the Stop closure can be referenced. Single-fire: once a kick
	// targeted at this client has been observed and kickFn invoked, later
	// kick ops on the receive path are ignored. The connection itself is
	// being torn down anyway.
	kickMu    sync.Mutex
	kickFn    func()
	kickFired bool

	// kickGate, when non-nil, is tee'd on every entity / space envelope
	// SendString sees. Used by cloud-mixer's gateway to keep its
	// auth-time refusal gate up to date. spatial-mixer leaves this
	// nil because the gate Apply is performed centrally in
	// DirectBackend.handleStringMessage. Idempotent under redundant
	// deliveries from multiple per-connection subscribers.
	kickGate *KickGate
}

// NewBouncerClient builds the per-connection client and starts its
// periodic sendInfo goroutine. A location updater must be passed here
// rather than assigned to the LocationUpdater field afterwards: the
// goroutine reads it from its first tick, so a post-construction write
// races. The parameter is variadic only for backward compatibility with
// callers that don't use one (cloud-mixer); pass at most one.
func NewBouncerClient(nodeConfig common.NodeConfig,
	bouncer IMessageSenderReceiver,
	locationUpdater ...ILocationUpdater,
) *BouncerClient {

	client := BouncerClient{}
	client.volumeCounter = 0
	client.volumeSum = 0.0
	client.nodeConfig = nodeConfig
	client.sentKeys = make(map[string]bool)
	client.sentEntityKeys = make(map[string]bool)
	if len(locationUpdater) > 0 {
		client.LocationUpdater = locationUpdater[0]
	}
	pClient := &client
	client.bouncer = bouncer
	client.bouncer.SetReceiveSender(pClient)
	client.position = nodeConfig.Position
	client.rotation = nodeConfig.Rotation
	pClient.sendInfo()

	return pClient
}

// Depart announces this client's disappearance and quiesces
// immediately — the phase-6 replacement for the grace-window Stop. One
// Gone datagram, one tombstone sweep, no timing-based behaviour.
func (client *BouncerClient) Depart() {
	client.AnnounceDisappearance()
	client.Quiesce()
}

// Quiesce stops the periodic sendInfo loop and detaches the receive
// sender WITHOUT announcing anything — the departure announcement is
// DepartNode's job (plan/history/state-cleanup phase 3). Idempotent.
func (client *BouncerClient) Quiesce() {
	client.done.Store(true)
	client.rsMu.Lock()
	client.receiveSender = nil
	client.rsMu.Unlock()
}

// Republish immediately re-emits this client's entity record and
// attributes (plan/history/state-cleanup phase 6b). Used after a bouncer
// reconnect (E12): the restarted bouncer's cache is empty, and waiting
// for the periodic ~4 s re-emit leaves joiners backfilling a hole.
// No-op once the client has quiesced — a departed node must not
// resurrect from a late reconnect callback.
func (client *BouncerClient) Republish() {
	if client.done.Load() {
		return
	}
	client.sendEntity()
	client.sendAttributes()
}

// TakeSentKeys returns and clears the attribute / entity keys this
// client has published. DepartNode's announce uses it as the tombstone
// source on cacheless backends (DirectRocBackend) and unions it with
// the subject-tracked keys on cache builds. Clearing mirrors
// AnnounceDisappearance, so a later call returns nothing.
func (client *BouncerClient) TakeSentKeys() (attrs []string, entity []string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	for key := range client.sentKeys {
		attrs = append(attrs, key)
	}
	client.sentKeys = make(map[string]bool)
	for key := range client.sentEntityKeys {
		entity = append(entity, key)
	}
	client.sentEntityKeys = make(map[string]bool)
	return attrs, entity
}

func (client *BouncerClient) SetVolume(src []float32) {
	var sum float32 = 0.0
	count := len(src)
	for i := 0; i < count; i++ {
		sum += src[i] * src[i]
	}

	rms := math.Sqrt(float64(sum) / float64(count))
	client.mu.Lock()
	defer client.mu.Unlock()
	client.volumeSum += rms
	//fmt.Printf("rms: %f\n", rms)
	client.volumeCounter += 1

}

// SetReceiveSender installs the per-connection writer that receives
// cache backfill and live broadcasts for this client. The first call
// (the "attach") also flushes any messages that arrived during the
// pre-attach window — see the rsMu / pendingStrings comment on the
// struct.
func (client *BouncerClient) SetReceiveSender(receiveSender IMessageSender) {
	client.rsMu.Lock()
	wasAttached := client.attached
	client.receiveSender = receiveSender
	client.attached = true

	var pStrs []pendingString
	var pData []pendingData
	if !wasAttached && receiveSender != nil {
		pStrs = client.pendingStrings
		pData = client.pendingData
		client.pendingStrings = nil
		client.pendingData = nil
		client.pendingBytes = 0
	}
	client.rsMu.Unlock()

	for _, p := range pStrs {
		receiveSender.SendString(p.topic, p.msg)
	}
	for _, p := range pData {
		receiveSender.SendData(p.topic, p.data)
	}
}

func (client *BouncerClient) SetPosition(position common.Position) {

	client.mu.Lock()
	defer client.mu.Unlock()
	client.position = position
}

func (client *BouncerClient) SetRotation(rotation common.Rotation) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.rotation = rotation
}

func (client *BouncerClient) AnnounceDisappearance() {

	client.mu.Lock()
	client.gone = 1
	nodeInfo := common.NodeInfo3{
		Uuid:     client.nodeConfig.Uuid,
		Position: client.position,
		Rotation: client.rotation,
		Volume:   client.volume,
		Gone:     client.gone}

	// Collect keys to tombstone while holding the lock.
	tombstoneOps := make([][]byte, 0, len(client.sentKeys))
	for key := range client.sentKeys {
		if tb, err := statecache.BuildTombstoneOp(key); err == nil {
			tombstoneOps = append(tombstoneOps, tb)
		}
	}
	client.sentKeys = make(map[string]bool)

	entityTombstoneOps := make([][]byte, 0, len(client.sentEntityKeys))
	for key := range client.sentEntityKeys {
		if tb, err := statecache.BuildTombstoneOp(key); err == nil {
			entityTombstoneOps = append(entityTombstoneOps, tb)
		}
	}
	client.sentEntityKeys = make(map[string]bool)
	client.mu.Unlock()

	common.LogDebug("Node going away")

	// Send batch tombstones for all attribute keys we've set.
	if len(tombstoneOps) > 0 {
		var msg []byte
		if len(tombstoneOps) == 1 {
			msg = tombstoneOps[0]
		} else {
			msg, _ = statecache.BuildBatch(tombstoneOps)
		}
		if len(msg) > 0 {
			client.bouncer.SendString("attributes", string(msg))
		}
	}

	// Tombstone all entity keys we've set so gateways drop our subspace info
	// and remove us from the visibility map.
	if len(entityTombstoneOps) > 0 {
		var msg []byte
		if len(entityTombstoneOps) == 1 {
			msg = entityTombstoneOps[0]
		} else {
			msg, _ = statecache.BuildBatch(entityTombstoneOps)
		}
		if len(msg) > 0 {
			client.bouncer.SendString("entity", string(msg))
		}
	}

	client.bouncer.SendData("state", inout.NodeInfo3ToBytes(nodeInfo))
}

func (client *BouncerClient) sendAttributes() {
	uuidStr := client.nodeConfig.Uuid.String()

	ops := make([][]byte, 0, 8)
	keys := make([]string, 0, 8)

	addOp := func(key string, value interface{}) {
		if op, err := statecache.BuildOp(key, value); err == nil {
			ops = append(ops, op)
			keys = append(keys, key)
		}
	}

	addOp(uuidStr+".name", client.nodeConfig.Name)
	if ticket, ok := client.nodeConfig.Attrs["ticket"]; ok {
		addOp(uuidStr+".ticket", ticket)
	}
	if conn, ok := client.nodeConfig.Attrs["connection"]; ok {
		addOp(uuidStr+".connection", conn)
	}

	if len(ops) == 0 {
		return
	}

	// Track sent keys for tombstoning on disconnect.
	client.mu.Lock()
	for _, key := range keys {
		client.sentKeys[key] = true
	}
	client.mu.Unlock()

	var msg []byte
	if len(ops) == 1 {
		msg = ops[0]
	} else {
		var err error
		msg, err = statecache.BuildBatch(ops)
		if err != nil {
			common.LogWarn("sendAttributes: failed to build batch: %v", err)
			return
		}
	}

	client.bouncer.SendString("attributes", string(msg))
}

// sendEntity broadcasts this node's server-internal config on the cached
// "entity" topic, as flat per-field operations. Gateways receive these to
// drive subspace filtering on outgoing attributes/state, and (via the
// per-client read path) forward each client's own keys back to it.
//
// Schema:
//   - `{uuid}`                    — existence marker (set/tombstone)
//   - `{uuid}.subspaces.{ss_id}`  — one op per subspace this node is in
//   - `{uuid}.roles.{role_name}`  — one op per role this ticket holds
//
// The marker key carries no data; its sole purpose is to give
// disappearance a reliable "node has gone" tombstone target even when
// no other entity fields are populated. Roles use the same set-of-keys
// shape as subspaces (consistent with the mutes/solos pattern in the
// entity record) so they support arbitrary multi-role tickets without
// list re-encoding.
func (client *BouncerClient) sendEntity() {
	uuidStr := client.nodeConfig.Uuid.String()

	capacity := 1 + len(client.nodeConfig.SubSpaces) + len(client.nodeConfig.Roles)
	ops := make([][]byte, 0, capacity)
	keys := make([]string, 0, capacity)

	addOp := func(key string, value interface{}) {
		if op, err := statecache.BuildOp(key, value); err == nil {
			ops = append(ops, op)
			keys = append(keys, key)
		}
	}

	addOp(uuidStr, true)
	for _, ss := range client.nodeConfig.SubSpaces {
		addOp(uuidStr+".subspaces."+ss.String(), true)
	}
	for _, role := range client.nodeConfig.Roles {
		if role == "" {
			continue
		}
		addOp(uuidStr+".roles."+role, true)
	}

	if len(ops) == 0 {
		return
	}

	client.mu.Lock()
	for _, key := range keys {
		client.sentEntityKeys[key] = true
	}
	client.mu.Unlock()

	var msg []byte
	if len(ops) == 1 {
		msg = ops[0]
	} else {
		var err error
		msg, err = statecache.BuildBatch(ops)
		if err != nil {
			common.LogWarn("sendEntity: failed to build batch: %v", err)
			return
		}
	}

	client.bouncer.SendString("entity", string(msg))
}

// SetCommandHandler installs the registry + authorizer used by
// HandleCommand. It's a setter rather than a constructor argument so
// the many existing NewBouncerClient call sites don't all need to be
// updated at once. Pass commands.DefaultRegistry() / DefaultAuthorizer()
// for production wiring.
func (client *BouncerClient) SetCommandHandler(registry *commands.Registry, authorizer commands.Authorizer) {
	client.cmdRegistry = registry
	client.cmdAuthorizer = authorizer
}

// SetKickFn installs the closure that closes this client's session
// when a targeted kick op is observed on the receive path. Called
// late-bound by NewConnectionHandler once the handler exists and its
// Stop method can be captured. Until installed, kick ops are forwarded
// downstream as normal but no live disconnect fires.
func (client *BouncerClient) SetKickFn(fn func()) {
	client.kickMu.Lock()
	defer client.kickMu.Unlock()
	client.kickFn = fn
}

// SetKickGate attaches a process-shared KickGate. When set, the
// receive-side dispatcher tees every kick-relevant op into the gate so
// auth-time refusal stays up to date. Used by cloud-mixer; in
// spatial-mixer the gate is updated centrally in DirectBackend so this
// is left nil.
func (client *BouncerClient) SetKickGate(gate *KickGate) {
	client.kickMu.Lock()
	defer client.kickMu.Unlock()
	client.kickGate = gate
}

// inspectIncomingEnvelope decodes a receive-path envelope on the
// entity / space topic, runs the kick-detection rules for this
// client, and tees each op into the attached KickGate (if any).
// Called from SendString before the envelope is forwarded to the
// per-connection writer. The single-fire kickFn invariant holds:
// once a kick targeted at this client has been seen, repeat
// deliveries are ignored.
//
// Targeting rules for kickFn:
//   - entity: `{myUuid}.kicked` set (tombstones are unkicks → ignored)
//   - space:  `roles-kicked.{R}` set, R ∈ this client's roles
func (client *BouncerClient) inspectIncomingEnvelope(topic, jsonString string) {
	client.kickMu.Lock()
	gate := client.kickGate
	fired := client.kickFired
	fn := client.kickFn
	client.kickMu.Unlock()

	// Cheap pre-check — if there's nothing to do, skip the envelope
	// parse entirely.
	if gate == nil && (fired || fn == nil) {
		return
	}

	payload := []byte(jsonString)
	if !statecache.IsCacheEnvelope(payload) {
		return
	}
	envelope, err := statecache.Decode(payload)
	if err != nil {
		return
	}
	ops, rawOps, err := statecache.ParseOps(envelope.Value)
	if err != nil {
		return
	}

	myUUID := client.nodeConfig.Uuid
	shouldKick := false

	for i, op := range ops {
		if gate != nil {
			var value []byte
			if !op.Tombstone {
				value = statecache.ExtractOpValue(rawOps[i])
			}
			gate.Apply(topic, statecache.Op{
				Topic:     topic,
				Key:       op.Key,
				Value:     value,
				OpID:      envelope.OpID,
				Tombstone: op.Tombstone,
			})
		}

		if shouldKick || op.Tombstone {
			continue
		}
		switch topic {
		case "entity":
			id, rest, ok := splitEntityKey(op.Key)
			if !ok || rest != "kicked" {
				continue
			}
			if id == myUUID {
				shouldKick = true
			}
		case "space":
			const prefix = "roles-kicked."
			if !strings.HasPrefix(op.Key, prefix) {
				continue
			}
			role := op.Key[len(prefix):]
			if role == "" {
				continue
			}
			for _, r := range client.nodeConfig.Roles {
				if r == role {
					shouldKick = true
					break
				}
			}
		}
	}

	if !shouldKick {
		return
	}

	client.kickMu.Lock()
	if client.kickFired || client.kickFn == nil {
		client.kickMu.Unlock()
		return
	}
	client.kickFired = true
	fnNow := client.kickFn
	client.kickMu.Unlock()

	// Invoke outside the lock — Stop may end up calling back into
	// this BouncerClient (e.g. via AnnounceDisappearance).
	fnNow()
}

// HandleControlMessage routes an inbound control-channel message. The
// only supported type is "command", which carries
// `{message: {command, args}}` and is dispatched through HandleCommand.
func (client *BouncerClient) HandleControlMessage(msg common.ControlMessage) {
	if msg.MessageType != "command" {
		common.LogDebug("HandleControlMessage: unknown type %q", msg.MessageType)
		return
	}
	// msg.Message is a generic map; marshal it back to JSON so we can
	// decode into the typed envelope.
	inner, err := json.Marshal(msg.Message)
	if err != nil {
		common.LogDebug("HandleControlMessage: failed to re-marshal command message: %v", err)
		return
	}
	var env struct {
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(inner, &env); err != nil {
		common.LogDebug("HandleControlMessage: bad command envelope: %v", err)
		return
	}
	if env.Command == "" {
		common.LogDebug("HandleControlMessage: missing command name")
		return
	}
	client.HandleCommand(env.Command, env.Args)
}

// HandleCommand dispatches a control-channel command for the holder of
// this connection's ticket. Resolution order:
//
//  1. Look up the command in the registry.
//  2. Check the holder's roles via the authorizer.
//  3. Build the op (decode + validate + emit).
//  4. Encode the op as a statecache JSON op (set or tombstone).
//  5. Send via bouncer.SendString(op.Topic, encoded) — same path as
//     server-internal entity writes — so the op flows through the
//     cache → broadcast → per-client filter pipeline.
//
// All failures (unknown command, not authorised, bad args, encode
// error) result in a silent drop: a debug-level log line is emitted
// for diagnostics but no feedback flows back to the client. This is
// the strict-MVC contract — clients infer rejection from the absence
// of an echoed op.
func (client *BouncerClient) HandleCommand(name string, args json.RawMessage) {
	if client.cmdRegistry == nil || client.cmdAuthorizer == nil {
		common.LogDebug("HandleCommand: dispatcher not installed, dropping %q", name)
		return
	}
	op, err := commands.Dispatch(client.cmdRegistry, client.cmdAuthorizer, name, args, client.nodeConfig.Uuid, client.nodeConfig.Roles)
	if err != nil {
		common.LogDebug("HandleCommand: %v", err)
		return
	}

	var encoded []byte
	if op.Tombstone {
		encoded, err = statecache.BuildTombstoneOp(op.Key)
	} else {
		encoded, err = statecache.BuildOp(op.Key, op.Value)
	}
	if err != nil {
		common.LogWarn("HandleCommand: encode failed for %q: %v", name, err)
		return
	}
	client.bouncer.SendString(op.Topic, string(encoded))
}

func (client *BouncerClient) sendInfo() {
	go func() {

		ticker := timing.NewTicker(100, false)
		counter := 0
		for !client.done.Load() {
			client.mu.Lock()

			var vol float64

			if client.volumeCounter == 0 {
				vol = 0.0
			} else {
				vol = client.volumeSum / float64(client.volumeCounter)
				client.volumeSum = 0.0
				client.volumeCounter = 0
			}

			if vol > client.volume-0.01 {
				client.volume = vol
			} else {
				client.volume -= 0.01
			}

			//if client.rotation.Roll != 0.0 {
			//	fmt.Printf("client.rotation.Roll: %v", client.rotation.Roll)
			//}

			nodeInfo := common.NodeInfo3{
				Uuid:     client.nodeConfig.Uuid,
				Position: client.position,
				Rotation: client.rotation,
				Volume:   client.volume,
				Gone:     client.gone}

			client.mu.Unlock()

			client.bouncer.SendData("state", inout.NodeInfo3ToBytes(nodeInfo))
			if client.LocationUpdater != nil {
				client.LocationUpdater.UpdateLocation(client.nodeConfig.Uuid, client.position, client.rotation)
			}

			if counter == 0 {
				// Entity first — gateways need our subspaces in
				// nodeSubspaces before the attributes envelope arrives,
				// otherwise the filter drops the attribute.
				client.sendEntity()
				client.sendAttributes()
			}
			if counter > 40-2 {
				counter = -1
			}
			counter++

			ticker.Tick()
		}
	}()
}

func (client *BouncerClient) SendString(eventName string, jsonString string) {
	// Tee into the kick detector + KickGate before the forward path.
	// The envelope is still delivered downstream so writers see it as
	// normal — the kick only changes connection lifecycle and
	// auth-time refusal, not message visibility.
	if eventName == "entity" || eventName == "space" {
		client.inspectIncomingEnvelope(eventName, jsonString)
	}

	client.rsMu.Lock()
	rs := client.receiveSender
	if rs == nil {
		// Pre-attach: buffer for the eventual SetReceiveSender. The
		// post-attach nil case (Stop) intentionally drops — see
		// `attached` on the struct.
		if !client.attached {
			size := len(jsonString)
			for client.pendingBytes+size > pendingMaxBytes && len(client.pendingStrings) > 0 {
				client.pendingBytes -= len(client.pendingStrings[0].msg)
				client.pendingStrings = client.pendingStrings[1:]
			}
			client.pendingStrings = append(client.pendingStrings, pendingString{topic: eventName, msg: jsonString})
			client.pendingBytes += size
		}
		client.rsMu.Unlock()
		return
	}
	client.rsMu.Unlock()
	rs.SendString(eventName, jsonString)
}

func (client *BouncerClient) SendData(eventName string, data []byte) {
	client.rsMu.Lock()
	rs := client.receiveSender
	if rs == nil {
		if !client.attached {
			cp := make([]byte, len(data))
			copy(cp, data)
			for client.pendingBytes+len(cp) > pendingMaxBytes && len(client.pendingData) > 0 {
				client.pendingBytes -= len(client.pendingData[0].data)
				client.pendingData = client.pendingData[1:]
			}
			client.pendingData = append(client.pendingData, pendingData{topic: eventName, data: cp})
			client.pendingBytes += len(cp)
		}
		client.rsMu.Unlock()
		return
	}
	client.rsMu.Unlock()
	rs.SendData(eventName, data)
}
