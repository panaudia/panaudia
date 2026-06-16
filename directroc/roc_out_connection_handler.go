package directroc

import (
	"sync"
	"unsafe"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/roc"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/direct"
	"github.com/panaudia/panaudia/spacer"
)

type RocOutConnectionHandler struct {
	isActive                  bool
	channelCount              int
	order                     int
	sender                    *roc.RocOutput
	interlacedAmbisonicBuffer []float32
	schmidt                   bool
	bouncerClient             *space.BouncerClient
	backend                   *DirectRocBackend
	nodeId                    uuid.UUID
	registryEntry             *sessions.Entry
	stopOnce                  sync.Once
}

func (handler *RocOutConnectionHandler) ChannelCount() uint32 {
	return uint32(handler.channelCount)
}

func (handler *RocOutConnectionHandler) Connect() {
	handler.sender.Connect()
}

func (handler *RocOutConnectionHandler) StartKeepAlive() {

}

// Stop tears the ROC output node down; announcement, mixer removal and
// release run through DepartNode. Idempotent (Kill's KillFn is this
// Stop).
func (handler *RocOutConnectionHandler) Stop() {
	handler.stopOnce.Do(func() {
		handler.isActive = false
		handler.sender.Stop()
		handler.backend.DepartNode(handler.registryEntry, direct.ReasonStopped)

		handler.backend.Lock()
		delete(handler.backend.rocOutHandlersByUuid, handler.nodeId)
		handler.backend.Unlock()
	})
}

func (handler *RocOutConnectionHandler) SetPosition(track uint32, position common.Position) {
	handler.bouncerClient.SetPosition(position)
}

func (handler *RocOutConnectionHandler) SetRotation(track uint32, rotation common.Rotation) {

}

func (handler *RocOutConnectionHandler) IsActive() bool {
	return handler.isActive
}

func (handler *RocOutConnectionHandler) BeforeDestroy() {
	//handler.encoder.BeforeDestroy()
}

func (handler *RocOutConnectionHandler) WriteAmbisonic(ambisonicChannels []float32) {
	//common.LogDebug("WriteAmbisonic channels: %v", handler.channelCount)
	if handler.schmidt {
		spacer.Panaudia_utils_convertN3DToSN3D(uintptr(unsafe.Pointer(&ambisonicChannels[0])), handler.order, common.FRAME_SIZE)
	}

	for i := 0; i < handler.channelCount; i++ {
		for j := 0; j < common.FRAME_SIZE; j++ {
			handler.interlacedAmbisonicBuffer[j*handler.channelCount+i] = ambisonicChannels[i*common.FRAME_SIZE+j]
		}
	}

	handler.sender.Writef32(handler.interlacedAmbisonicBuffer)
}
