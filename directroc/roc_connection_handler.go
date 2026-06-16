package directroc

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/roc"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/direct"
)

// rocAliveTimeout: a ROC input session with no inbound packets for this
// long reports !Alive(). ROC is connectionless — packet activity is its
// only liveness signal. (Comfortably above the mixer's 400-tick ≈ 2 s
// stale-node threshold; consumed by the reconciler from phase 4.)
const rocAliveTimeout = 5 * time.Second

type RocConnectionHandlerDelegate interface {
	setInputForUuid(input *RocTrackInput, id uuid.UUID)
	NewNode(nodeConfig common.NodeConfig, withOutput bool) *common.ServerError
}

type RocConnectionHandler struct {
	bouncerClients  []*space.BouncerClient
	nodeUuids       []uuid.UUID
	trackCount      uint32
	isActive        bool
	receiver        *roc.RocInput
	demuxer         *Demuxer
	backend         *DirectRocBackend
	registryEntries []*sessions.Entry
	stopOnce        sync.Once
}

func (handler *RocConnectionHandler) Connect() {
	handler.receiver.Connect()
}

func (handler *RocConnectionHandler) BeforeDestroy() {

}

// Stop tears the whole ROC connection down (all tracks). Each node's
// departure — announcement (the per-node BouncerClient's sent keys are
// the tombstone source on this cacheless backend), mixer removal,
// bouncer/registry release — runs through DepartNode, so ROC gains the
// disappearance announcement it previously skipped (findings §3).
// Idempotent: Kill (whose KillFn is this Stop) and an admin Stop can
// race.
func (handler *RocConnectionHandler) Stop() {
	handler.stopOnce.Do(func() {
		handler.isActive = false
		handler.receiver.Stop()
		common.LogDebug("ROC connection handler stopped")

		for _, entry := range handler.registryEntries {
			handler.backend.DepartNode(entry, direct.ReasonStopped)
		}

		handler.backend.Lock()
		for _, id := range handler.nodeUuids {
			delete(handler.backend.rocTrackInputsByUuid, id)
		}
		handler.backend.Unlock()
	})
}

func (handler *RocConnectionHandler) SetPosition(track uint32, position common.Position) {

	if track < handler.trackCount {
		handler.bouncerClients[track].SetPosition(position)
	}
}

func (handler *RocConnectionHandler) SetRotation(track uint32, rotation common.Rotation) {

	if track < handler.trackCount {
		handler.bouncerClients[track].SetRotation(rotation)
	}
}

func (handler *RocConnectionHandler) Ports() common.RocPorts {
	return common.RocPorts{"localhost",
		handler.receiver.SourcePort,
		handler.receiver.RepairPort,
		handler.receiver.ControlPort}
}

func (handler *RocConnectionHandler) IsActive() bool {
	return handler.isActive
}

func (handler *RocConnectionHandler) Configure(config common.RocConfig) {

	for i, nodeConfig := range config.Nodes {
		handler.backend.rocTrackInputsByUuid[nodeConfig.Uuid] = handler.demuxer.TrackInputs[i]

		err := handler.backend.NewNode(nodeConfig, false)

		if err != nil {
			common.LogError("NewNode error: %v", err)
		}

		handler.nodeUuids = append(handler.nodeUuids, nodeConfig.Uuid)
		bouncer := direct.NewBouncer(nodeConfig.Uuid, handler.backend.StringChIn, handler.backend.DataChIn)
		bouncerClient := space.NewBouncerClient(nodeConfig, bouncer, handler.backend)
		handler.bouncerClients = append(handler.bouncerClients, bouncerClient)
		handler.backend.BouncersByUuid[nodeConfig.Uuid] = bouncer
		handler.demuxer.BouncerClients = handler.bouncerClients

		// Session liveness registry (phase 2: passive). All tracks of a
		// ROC connection share the receiver, so Alive is the shared
		// demuxer's packet activity and Kill tears the whole handler
		// down.
		live := &sessions.FuncSession{
			AliveFn: func() bool { return handler.demuxer.LastPacketAge() < rocAliveTimeout },
			KillFn: func(reason string) {
				common.LogInfo("Killing ROC session %s: %s", nodeConfig.Uuid, reason)
				handler.Stop()
			},
		}
		_, entry := handler.backend.Sessions.Register(nodeConfig.Uuid, live, "roc")
		handler.registryEntries = append(handler.registryEntries, entry)
	}
}
