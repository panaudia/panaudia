package panaudia_server

import (
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/pion/webrtc/v3/pkg/media"
)

// TrackWriter is a generic interface for writing audio samples to output tracks
// This allows different transport implementations (WebRTC, MOQ, etc.) to be used
// with the same ConnectionHandler implementation
type TrackWriter interface {
	WriteSample(sample media.Sample) error
}

type ConnectionHandler interface {
	WriteOpus(src []byte) error
	GetDeadSessionCh() chan uint64
	SetPosition(position common.Position)
	SetRotation(rotation common.Rotation)
	Connect() *common.ServerError
	Stop()
	IsActive() bool
	ControlMessage(msg common.ControlMessage)
	space.IMessageReceiver
}

type RocConnectionHandler interface {
	Connect()
	Stop()
	Ports() common.RocPorts
	IsActive() bool
	Configure(config common.RocConfig)
	SetPosition(track uint32, position common.Position)
	SetRotation(track uint32, rotation common.Rotation)
}

type RocOutConnectionHandler interface {
	Connect()
	//ConnectSender()
	Stop()
	IsActive() bool
	ChannelCount() uint32
	SetPosition(track uint32, position common.Position)
	SetRotation(track uint32, rotation common.Rotation)
}

type Backend interface {
	NewConnectionHandler(nodeConfig common.NodeConfig,
		outputTrack TrackWriter) ConnectionHandler
	NewRocConnectionHandler(trackCount uint32) RocConnectionHandler
	NewRocOutConnectionHandler(rocOutConfig common.RocOutputConfig) RocOutConnectionHandler
}

// ConnectionHandlerFactoryWithError is an optional extension of Backend.
// Backends that implement it let transports distinguish why an admission
// failed (duplicate identity vs server full) and reject the connection
// explicitly, and take the transport's LiveSession handle so the backend
// can own session registration and the departure funnel
// (plan/history/state-cleanup phase 3). Backends that don't (e.g. older
// cloud-mixer backends) keep working through NewConnectionHandler's nil
// return — kept separate so adding it is not a breaking interface change.
type ConnectionHandlerFactoryWithError interface {
	NewConnectionHandlerWithError(nodeConfig common.NodeConfig,
		outputTrack TrackWriter, live sessions.LiveSession, transport string) (ConnectionHandler, *common.ServerError)
}

// SessionRegistryProvider is an optional extension of Backend. Backends
// that implement it expose the session liveness registry
// (plan/history/state-cleanup, phase 2); transports register each admitted
// session and unregister on teardown. Backends that don't (e.g. older
// cloud-mixer backends) simply get no registration — kept separate so
// adding it is not a breaking interface change.
type SessionRegistryProvider interface {
	SessionRegistry() *sessions.Registry
}

// SessionRegistryOf returns the backend's registry, or nil when the
// backend doesn't provide one.
func SessionRegistryOf(backend interface{}) *sessions.Registry {
	if p, ok := backend.(SessionRegistryProvider); ok {
		return p.SessionRegistry()
	}
	return nil
}

// ConnectionHandlerFactory is the one method every backend flavour
// (full Backend, the MOQ server's narrower local interface) shares.
type ConnectionHandlerFactory interface {
	NewConnectionHandler(nodeConfig common.NodeConfig,
		outputTrack TrackWriter) ConnectionHandler
}

// NewConnectionHandlerE calls NewConnectionHandlerWithError when the
// backend provides it, falling back to NewConnectionHandler (with a
// generic error on nil, live/transport unused) otherwise.
func NewConnectionHandlerE(backend ConnectionHandlerFactory, nodeConfig common.NodeConfig,
	outputTrack TrackWriter, live sessions.LiveSession, transport string) (ConnectionHandler, *common.ServerError) {
	if factory, ok := backend.(ConnectionHandlerFactoryWithError); ok {
		return factory.NewConnectionHandlerWithError(nodeConfig, outputTrack, live, transport)
	}
	handler := backend.NewConnectionHandler(nodeConfig, outputTrack)
	if handler == nil {
		return nil, common.NewServerError(common.SERVER_ERROR_UNEXPECTED,
			map[string]string{"uuid": nodeConfig.Uuid.String()})
	}
	return handler, nil
}
