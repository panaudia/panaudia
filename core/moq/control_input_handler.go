package moq

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// ControlInputHandler reads control messages (mute/unmute etc.) from a client's
// control input track and forwards them to the ConnectionHandler.
type ControlInputHandler struct {
	track      *moqtransport.RemoteTrack
	nodeID     string
	trackNames *TrackNames

	connectionHandler interface {
		ControlMessage(msg common.ControlMessage)
	}

	ctx    context.Context
	cancel context.CancelFunc

	objectsReceived uint64
}

// NewControlInputHandler creates a new control input handler
func NewControlInputHandler(track *moqtransport.RemoteTrack, trackNames *TrackNames) *ControlInputHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &ControlInputHandler{
		track:      track,
		nodeID:     trackNames.NodeID,
		trackNames: trackNames,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetConnectionHandler wires the handler to the mixing backend
func (h *ControlInputHandler) SetConnectionHandler(handler interface{}) {
	if ch, ok := handler.(interface {
		ControlMessage(msg common.ControlMessage)
	}); ok {
		h.connectionHandler = ch
	}
}

// Start begins reading control messages
func (h *ControlInputHandler) Start() error {
	common.LogInfo("Starting control input handler for node %s", h.nodeID)
	go h.readLoop()
	return nil
}

// Stop stops reading control messages and closes the remote track —
// matching the audio and state input handlers (previously this track
// was never closed; plan/history/state-cleanup findings §7).
func (h *ControlInputHandler) Stop() error {
	h.cancel()

	if h.track != nil {
		if err := h.track.Close(); err != nil {
			return fmt.Errorf("failed to close control input track: %w", err)
		}
	}

	common.LogDebug("Control input handler stopped for node %s (received %d messages)", h.nodeID, h.objectsReceived)
	return nil
}

func (h *ControlInputHandler) readLoop() {
	for {
		obj, err := h.track.ReadObject(h.ctx)
		if err != nil {
			if h.ctx.Err() != nil {
				return // Context cancelled, normal shutdown
			}
			common.LogError("Control input read error for node %s: %v", h.nodeID, err)
			return
		}

		h.objectsReceived++
		h.processObject(obj)
	}
}

func (h *ControlInputHandler) processObject(obj *moqtransport.Object) {
	if len(obj.Payload) == 0 {
		return
	}

	// Parse JSON control message
	var msg common.ControlMessage
	if err := json.Unmarshal(obj.Payload, &msg); err != nil {
		common.LogError("Failed to parse control message from node %s: %v", h.nodeID, err)
		return
	}

	// Set the NodeId from the authenticated session (same as WebRTC does)
	msg.NodeId = h.nodeID

	common.LogDebug("Control message from node %s: type=%s", h.nodeID, msg.MessageType)

	if h.connectionHandler != nil {
		h.connectionHandler.ControlMessage(msg)
	}
}
