package direct

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/buffers"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/pion/webrtc/v3/pkg/media"
)

type ConnectionHandler struct {
	outputTrack    panaudia_server.TrackWriter
	buffer         buffers.ICircularBuffer
	decoder        *inout.OpusInputDecoder
	encoder        *inout.OpusOutputEncoder
	bouncerClient  *space.BouncerClient
	sampleDuration time.Duration
	isActive       atomic.Bool // advisory; lifecycle truth is registryEntry's state
	hasInput       bool
	backend        *DirectBackend
	nodeId         uuid.UUID
	readCounter    int
	lastStats      buffers.CircularBufferStats
	lastSnapshot   buffers.JitterBufferStats
	resumeOpID     uint64

	// registryEntry is this session's entry in the backend's session
	// registry — the lifecycle authority. Stop funnels into
	// backend.DepartNode through it; the entry's Live→Departing CAS
	// replaced the old stopOnce.
	registryEntry *sessions.Entry
}

func (handler *ConnectionHandler) Connect() *common.ServerError {
	return nil
}

func (handler *ConnectionHandler) WriteOpus(src []byte) error {
	if handler.hasInput {
		pcm := handler.decoder.Decode(src)
		handler.bouncerClient.SetVolume(pcm)
		handler.buffer.Write(pcm)
	}
	return nil
}

func (handler *ConnectionHandler) IsActive() bool {
	return handler.isActive.Load()
}

func (handler *ConnectionHandler) GetDeadSessionCh() chan uint64 {
	return nil
}

func (handler *ConnectionHandler) SetPosition(position common.Position) {
	handler.bouncerClient.SetPosition(position)
}

func (handler *ConnectionHandler) SetRotation(rotation common.Rotation) {
	handler.encoder.SetRotation(rotation)
	handler.bouncerClient.SetRotation(rotation)
}

func (handler *ConnectionHandler) GetTick() int64 {
	return 0
}

// Stop is the owner-goroutine funnel entry: the full departure runs via
// DepartNode (announce sweep, mixer removal, resource release). Safe to
// call from multiple goroutines and repeatedly — the registry entry's
// Live→Departing CAS admits exactly one departure per session.
func (handler *ConnectionHandler) Stop() {
	handler.backend.DepartNode(handler.registryEntry, ReasonTransportClosed)
}

func (handler *ConnectionHandler) SetReceiveSender(delegate space.IMessageSender) {
	handler.bouncerClient.SetReceiveSender(delegate)
	// Trigger backfill from the cache now that the receive path is ready
	handler.backend.BackfillBouncer(handler.nodeId, handler.resumeOpID)
}

// MonoInput and AmbisonicOutput interfaces

func (handler *ConnectionHandler) ReadMono(dst []float32) {
	if handler != nil {
		if handler.hasInput {
			handler.buffer.Read(dst)
			handler.readCounter++
			// Stats logging is gated on DEBUG (PANAUDIA_LOG_LEVEL <= 1) FIRST
			// so the snapshot is never computed under normal production levels.
			// At DEBUG it runs once per 200 reads (~1s at 5ms reads).
			if common.LogLevel <= common.LOG_LEVEL_DEBUG && handler.readCounter%200 == 0 {
				handler.logBufferStats()
			}
		}
	} else {
		common.LogWarn("handler: %v", handler)
	}
}

// logBufferStats emits a one-line buffer health summary at DEBUG. Callers
// MUST gate on common.LogLevel <= LOG_LEVEL_DEBUG before calling: the
// snapshot loads several atomics and does float conversions we don't want to
// pay for in normal production. For a JitterBuffer it logs the rich snapshot —
// the live window allowances L/H, the lap/drop/insert deltas, and the most
// recent window's breach counts (which side the window is being pushed on) —
// the fields that reveal what the adaptive window is doing; other buffer types
// fall back to the basic stats view.
func (handler *ConnectionHandler) logBufferStats() {
	if jb, ok := handler.buffer.(*buffers.JitterBuffer); ok {
		s := jb.Snapshot()
		last := handler.lastSnapshot
		common.LogDebug("[JBUF %s] fill=%.1fms L=%.1fms H=%.1fms started=%t "+
			"Δunder=%d Δover=%d Δlap=%d Δdrop=%d Δins=%d win(ins/drop)=%d/%d",
			handler.nodeId, s.FillMs, s.LowAllowanceMs, s.HighAllowanceMs, s.Started,
			s.Underruns-last.Underruns, s.Overruns-last.Overruns, s.Laps-last.Laps,
			s.SamplesDropped-last.SamplesDropped, s.SamplesInserted-last.SamplesInserted,
			s.LastWindowInserts, s.LastWindowDrops)
		handler.lastSnapshot = s
		return
	}

	stats := handler.buffer.GetStats()
	common.LogDebug("[CBUF %s] fill=%.1fms zone=%d Δunder=%d Δover=%d Δdrop=%d Δins=%d state=%s",
		handler.nodeId, stats.FillLevelMs, stats.CurrentZone,
		stats.UnderrunCount-handler.lastStats.UnderrunCount,
		stats.OverrunCount-handler.lastStats.OverrunCount,
		stats.SamplesDropped-handler.lastStats.SamplesDropped,
		stats.SamplesInserted-handler.lastStats.SamplesInserted,
		stats.State)
	handler.lastStats = stats
}

func (handler *ConnectionHandler) BeforeDestroy() {
	if handler.encoder != nil {
		handler.encoder.BeforeDestroy()
	}
}

func (handler *ConnectionHandler) WriteAmbisonic(ambisonicChannels []float32) {
	opusDataOut := handler.encoder.Encode(ambisonicChannels)

	if oggErr := handler.outputTrack.WriteSample(media.Sample{Data: opusDataOut, Duration: handler.sampleDuration}); oggErr != nil {
		common.LogError("outputTrack.WriteSample error: %v", oggErr)
	}
}

func (handler *ConnectionHandler) ControlMessage(msg common.ControlMessage) {
	if common.LogLevel <= common.LOG_LEVEL_DEBUG {
		common.LogDebug("ControlMessage: %v", msg)
	}
	handler.bouncerClient.HandleControlMessage(msg)
}
