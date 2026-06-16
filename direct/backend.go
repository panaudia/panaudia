package direct

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/binaural"
	"github.com/panaudia/panaudia/core/buffers"
	"github.com/panaudia/panaudia/core/commands"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/core/statecache"
)

type DirectBackend struct {
	ChannelCount   int
	ISpace         space.ISpace
	HandlersByUuid map[uuid.UUID]*ConnectionHandler
	BouncersByUuid map[uuid.UUID]*Bouncer
	StringChIn     chan StringMessage
	DataChIn       chan DataMessage
	Quit           chan int

	BinauralDecoderPool *binaural.BinauralDecoderPool

	// State cache
	Cache        *statecache.StateStore
	OpIDCounter  statecache.OpIDCounter
	CachePolicy  statecache.CachePolicy
	KeyExtractor statecache.KeyExtractor

	// ConnKeysBySubject tracks every connection-scoped cache key per
	// SUBJECT (the uuid the key is about — its leading uuid segment),
	// per topic, for bulk tombstoning when that subject's connection
	// departs. Authorship (msg.sourceUUID) is deliberately ignored:
	// keying cleanup by author meant a moderator's disconnect lifted
	// the mutes/kicks they had applied to others
	// (plan/history/state-cleanup/findings.md §2.2). Policy-scoped keys are
	// never tracked here — they survive every disconnect.
	// Shape: subject → topic → key set.
	ConnKeysBySubject map[uuid.UUID]map[string]map[string]bool

	// Command dispatch — shared across all BouncerClients constructed
	// from this backend. Built from commands.DefaultRegistry() and
	// commands.DefaultAuthorizer() at construction time.
	commandRegistry   *commands.Registry
	commandAuthorizer commands.Authorizer

	// Auth-time kick refusal and TTL sweeper — see core/space/kick_gate.go.
	// Tee'd from handleStringMessage; sweeper emits tombstones via
	// StringChIn so they flow through the standard cache + broadcast path.
	KickGate *space.KickGate

	// Sessions is the liveness authority (plan/history/state-cleanup phase 2/3):
	// every admitted session registers here, keyed by node uuid.
	// Admission serializes behind any non-Departed entry, and
	// DepartNode's per-entry CAS is the only departure gate.
	Sessions *sessions.Registry

	// pendingFree holds departed ConnectionHandlers between DepartNode
	// (any goroutine) and FreeSource (mixer goroutine, after the
	// NODE_CHANGE_DELETE is processed) — the encoder's cgo free must
	// only ever run on the mixer goroutine. Guarded by the backend
	// mutex.
	pendingFree map[uuid.UUID][]*ConnectionHandler

	// staleKills makes the stale-node Kill+escalation single-flight per
	// session generation (NotifyStaleNode fires every tick while a node
	// stays stale). Guarded by the backend mutex.
	staleKills map[uint64]bool

	// Evictions counts same-identity evictions (Q4) — observability:
	// a high rate suggests clients reconnecting without closing, or
	// credential sharing.
	Evictions atomic.Uint64

	stopOnce sync.Once

	sync.Mutex
}

func NewDirectBackend(channelCount int, maxSources int) *DirectBackend {
	backend := &DirectBackend{}
	backend.Initialise(channelCount, maxSources)
	return backend
}

// Initialise sets up every DirectBackend field (state cache, command
// dispatch, kick gate + sweeper) and starts the string/data dispatcher
// goroutine. It is shared by NewDirectBackend and directroc's
// NewDirectRocBackend (which embeds DirectBackend) so the two
// constructors cannot drift — the divergence that previously left the
// ROC backend with a nil state cache. Exported only so the embedding
// package can call it; treat it as construction-time only.
func (backend *DirectBackend) Initialise(channelCount int, maxSources int) {

	backend.ChannelCount = channelCount
	backend.HandlersByUuid = make(map[uuid.UUID]*ConnectionHandler)
	backend.StringChIn = make(chan StringMessage, 1000)
	backend.DataChIn = make(chan DataMessage, 1000)
	backend.Quit = make(chan int)
	backend.BouncersByUuid = make(map[uuid.UUID]*Bouncer)
	backend.ConnKeysBySubject = make(map[uuid.UUID]map[string]map[string]bool)
	backend.Sessions = sessions.NewRegistry()
	backend.pendingFree = make(map[uuid.UUID][]*ConnectionHandler)
	// One dedicated binaural decoder per possible output: the space caps
	// nodes at maxSources and every output leases a decoder for its
	// whole lifetime (claimed in newConnectionHandler, released in
	// OpusOutputEncoder.BeforeDestroy). Sizing below the max starved later
	// connections — GetDecoder returned nil and the render path nil-derefed,
	// so the pool must match the space's source cap exactly.
	backend.BinauralDecoderPool = binaural.NewBinauralDecoderPool(maxSources, channelCount)

	// Initialise state cache (env vars override defaults)
	backend.Cache = statecache.New(cacheConfigFromEnv())
	backend.CachePolicy = statecache.DefaultPolicy()
	backend.KeyExtractor = statecache.DefaultKeyExtractor()

	// Command dispatch — defaults are fine for now. Per-space role
	// permissions can be plumbed in later by overriding these fields.
	backend.commandRegistry = commands.DefaultRegistry()
	backend.commandAuthorizer = commands.DefaultAuthorizer()

	// KickGate — sweeper emits via StringChIn so its tombstones flow
	// through the same cache + broadcast pipeline as any other op.
	backend.KickGate = space.NewKickGate(func(topic string, encoded []byte) {
		backend.StringChIn <- StringMessage{topic: topic, msg: string(encoded)}
	})
	backend.KickGate.StartSweeper(time.Second)

	go func() {
		for {
			select {
			case msg := <-backend.StringChIn:
				backend.handleStringMessage(msg)
			case msg := <-backend.DataChIn:
				backend.handleDataMessage(msg)
			case <-backend.Quit:
				return
			}
		}
	}()
}

// handleStringMessage processes an incoming string message: caches if policy
// says so, then broadcasts to all bouncers.
//
// For cached topics, the message may be a single JSON operation or a batch
// (JSON array). Batches are split into individual ops for storage but
// broadcast as a single envelope carrying one opID.
func (backend *DirectBackend) handleStringMessage(msg StringMessage) {
	outMsg := msg

	if backend.CachePolicy != nil && backend.CachePolicy(msg.topic) {
		msgBytes := []byte(msg.msg)

		// Try to parse as ops (single or batch).
		ops, rawOps, err := statecache.ParseOps(msgBytes)
		if err != nil {
			common.LogDebug("cache op parse failed for topic %q: %v, forwarding unwrapped", msg.topic, err)
		} else {
			// Strict-MVC write enforcement: a per-client source may not
			// write (or tombstone) a connection-scoped key belonging to
			// another subject — such keys describe someone else's
			// connection and are swept on that subject's departure.
			// Cross-subject POLICY writes are legitimate (moderation
			// commands, authorised upstream in HandleCommand). System
			// writes (zero sourceUUID: KickGate sweeper, departure
			// sweeps) bypass.
			if msg.sourceUUID != (uuid.UUID{}) {
				keptOps := ops[:0]
				keptRaw := rawOps[:0]
				for i, op := range ops {
					scope, subject := commands.ClassifyKey(msg.topic, op.Key)
					if scope == commands.ScopeConnection && subject != uuid.Nil && subject != msg.sourceUUID {
						common.LogWarn("dropping cross-subject connection-scoped write from %s: topic %q key %q",
							msg.sourceUUID, msg.topic, op.Key)
						continue
					}
					keptOps = append(keptOps, op)
					keptRaw = append(keptRaw, rawOps[i])
				}
				if len(keptOps) == 0 {
					return
				}
				if len(keptOps) < len(ops) {
					// Rebuild the broadcast payload without the dropped ops.
					if len(keptRaw) == 1 {
						msgBytes = keptRaw[0]
					} else {
						rebuilt := make([][]byte, len(keptRaw))
						for i, r := range keptRaw {
							rebuilt[i] = r
						}
						batch, berr := statecache.BuildBatch(rebuilt)
						if berr != nil {
							common.LogWarn("cache batch rebuild after drop failed: %v", berr)
							return
						}
						msgBytes = batch
					}
				}
				ops, rawOps = keptOps, keptRaw
			}

			opID := backend.OpIDCounter.Assign()

			// Write each individual op to the store, and tee entity/space
			// ops into the BaseSpace mixer-effect queue. The space drains
			// the queue once per audio tick (BaseSpace.Process), translating
			// each op into the corresponding Encoder / role-state mutation.
			// See plan/history/commands/server-enforcement-plan.md for the rules.
			for i, op := range ops {
				if op.Tombstone {
					backend.Cache.Tomb(msg.topic, op.Key, opID, 0)
					backend.untrackConnKey(msg.topic, op.Key)
				} else {
					backend.Cache.Set(msg.topic, op.Key, rawOps[i], opID, 0)
					backend.trackConnKey(msg.topic, op.Key)
				}

				// Convention (see core/space/base_space_ops.go ApplyEntityOp):
				// the tee'd Op carries the JSON-encoded value field only,
				// not the full op envelope. Extract it from rawOps[i].
				var value []byte
				if !op.Tombstone {
					value = statecache.ExtractOpValue(rawOps[i])
				}
				cacheOp := statecache.Op{
					Topic:     msg.topic,
					Key:       op.Key,
					Value:     value,
					OpID:      opID,
					Tombstone: op.Tombstone,
				}

				if backend.ISpace != nil {
					switch msg.topic {
					case "entity":
						backend.ISpace.ApplyEntityOp(cacheOp)
					case "space":
						backend.ISpace.ApplySpaceOp(cacheOp)
					}
				}
				// Auth-time kick refusal — KickGate mirrors the entity /
				// space kick state from the cache. Tee runs alongside the
				// ISpace tee; same op, two consumers.
				if backend.KickGate != nil && (msg.topic == "entity" || msg.topic == "space") {
					backend.KickGate.Apply(msg.topic, cacheOp)
				}
			}

			// Wrap the original message (single or batch) in one envelope.
			// For broadcast we use the first op's key in the envelope header.
			envelopeOp := statecache.Op{
				Topic:     msg.topic,
				Key:       ops[0].Key,
				Value:     msgBytes,
				OpID:      opID,
				NodeID:    0,
				Tombstone: ops[0].Tombstone && len(ops) == 1,
			}
			encoded, err := statecache.Encode(envelopeOp)
			if err == nil {
				outMsg = StringMessage{topic: msg.topic, msg: string(encoded)}
			} else {
				common.LogWarn("cache encode error: %v", err)
			}
		}
	}

	backend.Lock()
	for _, bouncer := range backend.BouncersByUuid {
		bouncer.DeliverString(outMsg)
	}
	backend.Unlock()
}

// trackConnKey records a connection-scoped cache key under its subject
// (the key's leading uuid), for sweeping on that subject's departure.
// Policy-scoped and subject-less keys are not tracked. The map is
// written here on the dispatcher goroutine and read/deleted by
// FreeSource on the mixer goroutine, so all access is under the
// backend mutex.
func (backend *DirectBackend) trackConnKey(topic, key string) {
	scope, subject := commands.ClassifyKey(topic, key)
	if scope != commands.ScopeConnection || subject == uuid.Nil {
		return
	}
	backend.Lock()
	defer backend.Unlock()
	topics, ok := backend.ConnKeysBySubject[subject]
	if !ok {
		topics = make(map[string]map[string]bool)
		backend.ConnKeysBySubject[subject] = topics
	}
	keys, ok := topics[topic]
	if !ok {
		keys = make(map[string]bool)
		topics[topic] = keys
	}
	keys[key] = true
}

// untrackConnKey removes a tombstoned key from its subject's set.
func (backend *DirectBackend) untrackConnKey(topic, key string) {
	_, subject := commands.ClassifyKey(topic, key)
	if subject == uuid.Nil {
		return
	}
	backend.Lock()
	defer backend.Unlock()
	if topics, ok := backend.ConnKeysBySubject[subject]; ok {
		if keys, ok := topics[topic]; ok {
			delete(keys, key)
		}
	}
}

// handleDataMessage processes an incoming data message: caches if policy
// says so, then broadcasts to all bouncers.
func (backend *DirectBackend) handleDataMessage(msg DataMessage) {
	outMsg := msg

	if backend.CachePolicy != nil && backend.CachePolicy(msg.topic) {
		key, isTombstone, ok := backend.KeyExtractor(msg.topic, msg.msg)
		if ok {
			opID := backend.OpIDCounter.Assign()
			if isTombstone {
				backend.Cache.Tomb(msg.topic, key, opID, 0)
			} else {
				backend.Cache.Set(msg.topic, key, msg.msg, opID, 0)
			}

			op := statecache.Op{
				Topic:     msg.topic,
				Key:       key,
				Value:     msg.msg,
				OpID:      opID,
				NodeID:    0,
				Tombstone: isTombstone,
			}
			encoded, err := statecache.Encode(op)
			if err == nil {
				outMsg = DataMessage{topic: msg.topic, msg: encoded}
			} else {
				common.LogWarn("cache encode error: %v", err)
			}
		}
	}

	backend.Lock()
	for _, bouncer := range backend.BouncersByUuid {
		bouncer.DeliverData(outMsg)
	}
	backend.Unlock()
}

// BackfillBouncer sends all cached entries to a bouncer's output channels.
// Called when a new connection's receive sender is set up and ready.
//
// Topic ordering invariant: emit "entity" envelopes before "attributes",
// then "space", then anything else. The receiving writer (MoqDataWriter
// / DataWriter) only learns a source's subspaces from its entity
// envelope, and the attributes path filters via uuidVisible() — an
// attributes envelope arriving before its source's entity envelope
// would be silently dropped for any writer that has subspaces. The
// live path holds the same invariant by hand in
// core/space/bouncer_client.go (sendEntity is called immediately
// before sendAttributes in the per-tick loop). "space" has no
// upstream dependency (uuid-less keys, no visibility filter) and
// follows attributes. See plan/history/distributed-state-sync/topic-ordering.md.
func (backend *DirectBackend) BackfillBouncer(nodeID uuid.UUID, resumeOpID uint64) {
	backend.Lock()
	bouncer, ok := backend.BouncersByUuid[nodeID]
	backend.Unlock()
	if !ok {
		return
	}

	// Brief pause to let any in-flight stringChIn message reach the cache
	// before we snapshot. The dispatcher goroutine drains stringChIn and
	// calls cache.Set inside handleStringMessage; if a peer's
	// sendAttributes / sendEntity is queued or mid-handle when this
	// runs, the snapshot misses it and the connecting client doesn't
	// learn about that peer until the peer's next periodic re-emit
	// (~4 s in core/space/bouncer_client.go::sendInfo). 50 ms is the
	// same headroom cloud-mixer's bouncer uses for the equivalent race
	// (cloud-mixer/bouncer/server.go::handleBackfillREP).
	time.Sleep(50 * time.Millisecond)

	var ops []statecache.Op
	if resumeOpID > 0 {
		ops = backend.Cache.Since(resumeOpID)
	} else {
		ops = backend.Cache.Snapshot()
	}

	go func() {
		// Deduplicate: keep only the latest op per (topic, key) — highest opID wins
		latest := make(map[string]statecache.Op)
		for _, op := range ops {
			mapKey := op.Topic + "\x00" + op.Key
			if existing, ok := latest[mapKey]; !ok || op.OpID > existing.OpID {
				latest[mapKey] = op
			}
		}

		// Bucket by topic so we can emit in dependency order — see the
		// topic ordering invariant in this function's doc comment.
		// `space` has no upstream dependency (its keys are uuid-less,
		// so the writer's per-source visibility map is irrelevant) and
		// is emitted last alongside any future cached topics. If a
		// future topic depends on something *later* than attributes,
		// extend the order rather than relying on map iteration.
		var entity, attrs, space, other []statecache.Op
		for _, op := range latest {
			if op.Tombstone {
				continue // don't backfill tombstoned entries
			}
			switch op.Topic {
			case "entity":
				entity = append(entity, op)
			case "attributes":
				attrs = append(attrs, op)
			case "space":
				space = append(space, op)
			default:
				other = append(other, op)
			}
		}

		sendBucket := func(batch []statecache.Op) {
			for _, op := range batch {
				encoded, err := statecache.Encode(op)
				if err != nil {
					common.LogWarn("backfill encode error: %v", err)
					continue
				}
				bouncer.DeliverString(StringMessage{topic: op.Topic, msg: string(encoded)})
			}
		}
		sendBucket(entity)
		sendBucket(attrs)
		sendBucket(space)
		sendBucket(other)
	}()
}

// Stop shuts the dispatcher down. Idempotent: close(Quit) is single-fire
// (a quit *send* would block forever on a second call once the dispatcher
// has exited).
func (backend *DirectBackend) Stop() {
	backend.stopOnce.Do(func() {
		if backend.KickGate != nil {
			backend.KickGate.StopSweeper()
		}
		close(backend.Quit)
		if backend.Cache != nil {
			backend.Cache.Close()
		}
	})
}

func (backend *DirectBackend) SetSpace(iSpace space.ISpace) {
	backend.ISpace = iSpace

}

func (backend *DirectBackend) NewConnectionHandler(nodeConfig common.NodeConfig,
	outputTrack panaudia_server.TrackWriter) panaudia_server.ConnectionHandler {

	handler, _ := backend.NewConnectionHandlerWithError(nodeConfig, outputTrack, nil, "")
	if handler == nil {
		return nil
	}
	return handler
}

// admissionWaitTimeout bounds how long admission waits for a same-uuid
// departure in flight to complete. Variable so tests can shorten it.
var admissionWaitTimeout = 2 * time.Second

// NewConnectionHandlerWithError is NewConnectionHandler with the admission
// error surfaced, so transports can reject a duplicate-identity or
// server-full connect explicitly instead of silently half-admitting it
// (plan/history/state-cleanup/findings.md §2.3). Implements
// panaudia_server.ConnectionHandlerFactoryWithError.
//
// live is the transport's handle on the session (nil tolerated for
// legacy callers — a no-op session is synthesized); registration with
// the session registry happens here, after the admission-wait and
// before the node-add.
func (backend *DirectBackend) NewConnectionHandlerWithError(nodeConfig common.NodeConfig,
	outputTrack panaudia_server.TrackWriter, live sessions.LiveSession, transport string,
) (panaudia_server.ConnectionHandler, *common.ServerError) {

	realLive := live != nil
	if live == nil {
		live = &sessions.FuncSession{}
	}
	if transport == "" {
		transport = "unknown"
	}

	// Default to stereo (2) for backward compatibility with WebRTC
	inputChannels := nodeConfig.InputChannels
	if inputChannels == 0 {
		inputChannels = 2
	}

	// Admission-wait + eviction (mechanism-design §2, Q4): admission
	// serializes behind any non-Departed registry entry for this uuid;
	// a still-Live old session is killed first and the funnel runs its
	// full announced departure. The old session's NODE_CHANGE_DELETE is
	// enqueued before Departed() fires, so our ADD below is
	// FIFO-guaranteed to land after the removal, and our fresh writes
	// cannot be shadowed by the old session's tombstones. The KickGate
	// refusal runs at authentication, BEFORE this point on every
	// transport — a kicked uuid's rejected reconnect can never evict a
	// live session.
	outcome := backend.Sessions.Admit(nodeConfig.Uuid, admissionWaitTimeout,
		func(old *sessions.Entry) { old.Session.Kill(string(ReasonEvicted)) },
		func(old *sessions.Entry) { backend.DepartNode(old, ReasonEvicted) })
	if outcome.Evicted {
		backend.Evictions.Add(1)
	}
	if !outcome.OK {
		return nil, common.NewServerError(common.SERVER_ERROR_DUPLICATE, map[string]string{"uuid": nodeConfig.Uuid.String()})
	}
	waitedDeparture := outcome.Waited

	// Admission checks BEFORE any per-connection resources exist. The
	// BouncerClient announces presence (entity/attributes/NodeInfo3) from
	// the moment it is constructed, and the map writes below must not
	// overwrite a live session's entries. Everything from here to the
	// node-add runs under one lock hold so a concurrent same-uuid
	// admission cannot interleave.
	backend.Lock()
	defer backend.Unlock()

	// Re-check the registry under the lock: a concurrent admission for
	// the same uuid may have registered between the wait and here.
	if old := backend.Sessions.Get(nodeConfig.Uuid); old != nil && old.State() != sessions.Departed {
		common.LogWarn("Rejecting connection for %s: concurrent admission won", nodeConfig.Uuid)
		return nil, common.NewServerError(common.SERVER_ERROR_DUPLICATE, map[string]string{"uuid": nodeConfig.Uuid.String()})
	}

	// The space-level duplicate check guards against state with no
	// registry entry (orphans — the phase-4 reconciler's territory).
	// After a waited departure it must be skipped: the old node stays
	// in space.Nodes until the mixer tick processes its queued DELETE,
	// and FIFO already guarantees our ADD lands after it.
	if !waitedDeparture {
		if node, _ := backend.ISpace.GetNode(nodeConfig.Uuid); node != nil {
			common.LogWarn("Rejecting connection for %s: node already exists (duplicate session)", nodeConfig.Uuid)
			return nil, common.NewServerError(common.SERVER_ERROR_DUPLICATE, map[string]string{"uuid": nodeConfig.Uuid.String()})
		}
	}
	if backend.ISpace.SourceMaxReached() {
		common.LogWarn("Rejecting connection for %s: server full", nodeConfig.Uuid)
		return nil, common.NewServerError(common.SERVER_ERROR_FULL, map[string]string{"uuid": nodeConfig.Uuid.String()})
	}

	var buffer buffers.ICircularBuffer
	var encoder *inout.OpusOutputEncoder

	if nodeConfig.Input {
		// Frame-size-aware JitterBuffer geometry: caller declares W (writer
		// frame) and R (reader frame); LowInit is the warm-start guess for the
		// adaptive late-jitter allowance L, which then widens/narrows itself.
		var jcfg buffers.JitterBufferConfig
		if inputChannels == 1 {
			// MOQ: Opus packets up to 20 ms; audio render callback 5 ms;
			// MOQ datagrams are low-jitter so a tighter LowInit is appropriate.
			jcfg = buffers.JitterBufferConfig{
				SampleRate:      common.SAMPLE_RATE,
				NumChannels:     1,
				WriterFrameSize: 20 * time.Millisecond,
				ReaderFrameSize: 5 * time.Millisecond,
				LowInit:         5 * time.Millisecond,
			}
		} else {
			// WebRTC: 20 ms packets, jitter pre-smoothed by NetEQ but
			// still seed a slightly larger LowInit to absorb the bursty pattern.
			jcfg = buffers.JitterBufferConfig{
				SampleRate:      common.SAMPLE_RATE,
				NumChannels:     1,
				WriterFrameSize: 20 * time.Millisecond,
				ReaderFrameSize: 5 * time.Millisecond,
				LowInit:         10 * time.Millisecond,
			}
		}
		buffer = buffers.NewJitterBuffer(jcfg)

		binauralDecoder := backend.BinauralDecoderPool.GetDecoder(common.Rotation{})
		encoder = inout.NewOpusOutputEncoder(binauralDecoder, backend.ChannelCount)
	}
	decoder := inout.NewOpusInputDecoder(inputChannels)
	sampleDuration := (1000 / (common.SAMPLE_RATE / common.FRAME_SIZE)) * time.Millisecond

	bouncer := NewBouncer(nodeConfig.Uuid, backend.StringChIn, backend.DataChIn)
	bouncerClient := space.NewBouncerClient(nodeConfig, bouncer, backend)
	bouncerClient.SetCommandHandler(backend.commandRegistry, backend.commandAuthorizer)

	pHandler := &ConnectionHandler{outputTrack: outputTrack,
		buffer:         buffer,
		decoder:        decoder,
		encoder:        encoder,
		bouncerClient:  bouncerClient,
		sampleDuration: sampleDuration,
		hasInput:       nodeConfig.Input,
		backend:        backend,
		nodeId:         nodeConfig.Uuid,
		resumeOpID:     nodeConfig.ResumeOpID,
	}
	pHandler.isActive.Store(true)

	// Register with the session liveness registry. From here the entry
	// is the session's lifecycle authority: Stop/kick/timeout all
	// funnel into DepartNode through it.
	_, entry := backend.Sessions.Register(nodeConfig.Uuid, live, transport)
	pHandler.registryEntry = entry

	// Kick (E8): under the funnel a kick only severs the transport —
	// the owner goroutine notices and runs the departure. Legacy
	// callers without a transport handle keep the old direct-Stop
	// behaviour.
	if realLive {
		bouncerClient.SetKickFn(func() { live.Kill(string(ReasonKicked)) })
	} else {
		bouncerClient.SetKickFn(pHandler.Stop)
	}

	// Maps before node-add: the mixer tick may look the handler up
	// (GetInput/GetOutput) as soon as the ADD is processed.
	backend.HandlersByUuid[nodeConfig.Uuid] = pHandler
	backend.BouncersByUuid[nodeConfig.Uuid] = bouncer

	backend.ISpace.AddNodeStyledWithId(nodeConfig.Uuid, nodeConfig.Name, nodeConfig.Position, nodeConfig.SpaceNodeConfig)
	backend.ISpace.EnableOut(nodeConfig.Uuid)

	return pHandler, nil
}

func (backend *DirectBackend) NewRocConnectionHandler(trackCount uint32) panaudia_server.RocConnectionHandler {

	common.LogError("NewRocOutConnectionHandler in backend without it")
	return nil
}

func (backend *DirectBackend) NewRocOutConnectionHandler(rocOutConfig common.RocOutputConfig) panaudia_server.RocOutConnectionHandler {

	common.LogError("NewRocOutConnectionHandler in backend without it")
	return nil
}

func (backend *DirectBackend) NewNode(nodeConfig common.NodeConfig, withOutput bool) *common.ServerError {

	node, _ := backend.ISpace.GetNode(nodeConfig.Uuid)

	if node != nil {
		return common.NewServerError(common.SERVER_ERROR_DUPLICATE, map[string]string{})
	} else {

		if backend.ISpace.SourceMaxReached() {
			return common.NewServerError(common.SERVER_ERROR_FULL, map[string]string{})
		}

		backend.ISpace.AddNodeStyledWithId(nodeConfig.Uuid, nodeConfig.Name, nodeConfig.Position, nodeConfig.SpaceNodeConfig)
		if withOutput {
			backend.ISpace.EnableOut(nodeConfig.Uuid)
		}
	}
	return nil
}

func (backend *DirectBackend) UpdateLocation(Uuid uuid.UUID, position common.Position, rotation common.Rotation) {
	err := backend.ISpace.UpdateNode(Uuid, position, rotation)
	if err != nil {
		return
	}
}

// //// ISourceManager
func (backend *DirectBackend) EnsureSource(Uuid uuid.UUID,
	sourceDelegate common.UdpNodeIODelegate,
	statsDelegate buffers.BufferStatsDelegate,
	isRaw bool,
	hasInput bool) uint64 {
	// no need to do this here - direct sources created early
	return 0
}

func (backend *DirectBackend) GetInput(uuid uuid.UUID) inout.MonoInput {
	input, exists := backend.HandlersByUuid[uuid]
	//common.LogDebug("GetInput: %v", input)
	if exists {
		return input
	}
	return nil
}

func (backend *DirectBackend) GetOutput(uuid uuid.UUID) inout.AmbisonicOutput {

	output, exists := backend.HandlersByUuid[uuid]
	if exists {
		return output
	}

	return nil

}

// SessionRegistry implements panaudia_server.SessionRegistryProvider.
func (backend *DirectBackend) SessionRegistry() *sessions.Registry {
	return backend.Sessions
}

func (backend *DirectBackend) GetSessionIdForUuid(uuid uuid.UUID) (uint64, bool) {
	_, exists := backend.HandlersByUuid[uuid]
	return 0, exists
}

func (backend *DirectBackend) SetRotationForUuid(rotation common.Rotation, Uuid uuid.UUID) {
	// no need to do this here - rotation will have already been directly set
}

// FreeSource runs on the mixer goroutine when a node's removal is
// processed (doRemoveNode → node.BeforeDestroy). Since phase 3 the
// departure itself — announce, maps, bouncer, registry — is owned by
// DepartNode; this is left with exactly two jobs:
//
//  1. Funnel backstop: a removal that did NOT come through DepartNode
//     (today only the stale-node timeout path, until the phase-4
//     reconciler replaces it) still gets the full departure sweep.
//  2. The cgo invariant: the departed handler's encoder free must run
//     on this goroutine, never a connection goroutine — DepartNode
//     hands the handler over via pendingFree.
func (backend *DirectBackend) FreeSource(nodeUUID uuid.UUID) {

	if backend.Sessions != nil {
		if e := backend.Sessions.Get(nodeUUID); e != nil {
			backend.DepartNode(e, ReasonTimeout)
		}
	}

	backend.Lock()
	pending := backend.pendingFree[nodeUUID]
	delete(backend.pendingFree, nodeUUID)
	backend.Unlock()

	for _, handler := range pending {
		handler.BeforeDestroy()
	}
}

// cacheConfigFromEnv returns a statecache.Config with env var overrides.
//
//	PANAUDIA_CACHE_RING_SIZE         (default 16)
//	PANAUDIA_CACHE_SEGMENT_CAPACITY  (default 128)
//	PANAUDIA_CACHE_TOMBSTONE_TTL_SEC (default 30)
func cacheConfigFromEnv() statecache.Config {
	cfg := statecache.DefaultConfig()
	if v, err := strconv.Atoi(os.Getenv("PANAUDIA_CACHE_RING_SIZE")); err == nil && v > 0 {
		cfg.RingSize = v
	}
	if v, err := strconv.Atoi(os.Getenv("PANAUDIA_CACHE_SEGMENT_CAPACITY")); err == nil && v > 0 {
		cfg.SegmentCapacity = v
	}
	if v, err := strconv.Atoi(os.Getenv("PANAUDIA_CACHE_TOMBSTONE_TTL_SEC")); err == nil && v > 0 {
		cfg.TombstoneTTL = time.Duration(v) * time.Second
	}
	return cfg
}
