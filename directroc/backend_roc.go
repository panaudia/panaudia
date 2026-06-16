package directroc

import (
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/roc"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/direct"
)

type DirectRocBackend struct {
	rocOutHandlersByUuid map[uuid.UUID]*RocOutConnectionHandler
	rocTrackInputsByUuid map[uuid.UUID]*RocTrackInput
	registry             *roc.SlotRegistry
	direct.DirectBackend
}

func NewDirectRocBackend(channelCount int, maxSources int) *DirectRocBackend {

	backend := &DirectRocBackend{
		rocTrackInputsByUuid: make(map[uuid.UUID]*RocTrackInput),
		rocOutHandlersByUuid: make(map[uuid.UUID]*RocOutConnectionHandler),
		registry:             roc.NewRegistry(),
	}

	// Share DirectBackend's full construction (state cache, command
	// dispatch, kick gate + sweeper, and the standard cache+broadcast
	// dispatcher goroutine) instead of hand-rolling a subset. This is
	// what fixes the previously nil state cache and keeps the ROC
	// backend from drifting from the plain backend again. ROC state now
	// flows through the same cache/backfill path as MOQ and WebRTC.
	backend.DirectBackend.Initialise(channelCount, maxSources)

	return backend
}

func (backend *DirectRocBackend) NewRocConnectionHandler(trackCount uint32) panaudia_server.RocConnectionHandler {

	common.LogDebug("NewRocConnectionHandler")
	demuxer := NewDemuxer(int(trackCount), common.FRAME_SIZE)

	input := roc.NewRocInput("0.0.0.0",
		backend.registry,
		trackCount,
		demuxer,
		common.FRAME_SIZE)

	bouncerClients := make([]*space.BouncerClient, 0, trackCount)

	connectionHandler := RocConnectionHandler{receiver: input,
		bouncerClients: bouncerClients,
		demuxer:        demuxer,
		trackCount:     trackCount,
		isActive:       true,
		backend:        backend,
		nodeUuids:      make([]uuid.UUID, 0, trackCount),
	}

	return &connectionHandler
}

func (backend *DirectRocBackend) NewRocOutConnectionHandler(rocOutConfig common.RocOutputConfig) panaudia_server.RocOutConnectionHandler {

	common.LogDebug("NewRocOutConnectionHandler")

	sender := roc.NewRocOutput(rocOutConfig.Ports, uint32(rocOutConfig.Channels))

	bufferSize := rocOutConfig.Channels * common.FRAME_SIZE
	interlacedAmbisonicBuffer := make([]float32, bufferSize)

	bouncer := direct.NewBouncer(rocOutConfig.Node.Uuid, backend.StringChIn, backend.DataChIn)
	bouncerClient := space.NewBouncerClient(rocOutConfig.Node, bouncer, backend)

	connectionHandler := &RocOutConnectionHandler{isActive: true,
		channelCount:              rocOutConfig.Channels,
		order:                     common.OrderForChannelCount(rocOutConfig.Channels),
		sender:                    sender,
		interlacedAmbisonicBuffer: interlacedAmbisonicBuffer,
		schmidt:                   rocOutConfig.Normalisation == "SN3D",
		bouncerClient:             bouncerClient,
		backend:                   backend,
		nodeId:                    rocOutConfig.Node.Uuid,
	}

	backend.Lock()
	defer backend.Unlock()
	backend.rocOutHandlersByUuid[rocOutConfig.Node.Uuid] = connectionHandler

	backend.BouncersByUuid[rocOutConfig.Node.Uuid] = bouncer

	err := backend.NewNode(rocOutConfig.Node, true)

	if err != nil {
		common.LogError("NewNode error: %v", err)
		return nil
	}

	// Session liveness registry (phase 2: passive). A ROC output node
	// receives no inbound packets, so it has no activity signal: it is
	// considered alive until explicitly stopped.
	live := &sessions.FuncSession{
		KillFn: func(reason string) {
			common.LogInfo("Killing ROC output session %s: %s", rocOutConfig.Node.Uuid, reason)
			connectionHandler.Stop()
		},
	}
	_, connectionHandler.registryEntry = backend.Sessions.Register(rocOutConfig.Node.Uuid, live, "roc-out")

	return connectionHandler
}

func (backend *DirectRocBackend) GetInput(uuid uuid.UUID) inout.MonoInput {
	input, exists := backend.HandlersByUuid[uuid]
	//common.LogDebug("GetInput: %v", input)
	if exists {
		return input
	}
	rocInput, exists := backend.rocTrackInputsByUuid[uuid]
	if exists {
		return rocInput
	}
	return nil
}

func (backend *DirectRocBackend) GetOutput(uuid uuid.UUID) inout.AmbisonicOutput {

	output, exists := backend.HandlersByUuid[uuid]
	if exists {
		return output
	}

	rocOutput, rocExists := backend.rocOutHandlersByUuid[uuid]
	if rocExists {
		return rocOutput
	}

	return nil

}

// FreeSource is intentionally NOT overridden here. The embedded
// DirectBackend.FreeSource routes teardown through the departure funnel
// (DepartNode + pendingFree drain — see plan/history/state-cleanup and the
// CLAUDE.md "departure funnel" rule), and the ROC handlers self-remove
// from rocOutHandlersByUuid / rocTrackInputsByUuid in their own teardown
// paths (roc_out_connection_handler.go, roc_connection_handler.go). The
// previous override predated the funnel: it did ad-hoc cleanup and called
// Stop() on a nil bouncer for non-ROC (WebRTC/MOQ) nodes, which crashed.
