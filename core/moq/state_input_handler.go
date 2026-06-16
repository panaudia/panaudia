package moq

import (
	"context"
	"fmt"
	"io"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
)

// StateInputHandler handles incoming position/rotation state from a client's state track
type StateInputHandler struct {
	track      *moqtransport.RemoteTrack
	nodeID     string
	trackNames *TrackNames

	// ConnectionHandler interface (will be set in Phase 7)
	connectionHandler interface {
		SetPosition(position common.Position)
		SetRotation(rotation common.Rotation)
	}

	ctx    context.Context
	cancel context.CancelFunc

	// Stats
	objectsReceived uint64
	bytesReceived   uint64
	lastPosition    *common.Position
	lastRotation    *common.Rotation
}

// NewStateInputHandler creates a new state input handler
func NewStateInputHandler(track *moqtransport.RemoteTrack, trackNames *TrackNames) *StateInputHandler {
	ctx, cancel := context.WithCancel(context.Background())

	return &StateInputHandler{
		track:      track,
		nodeID:     trackNames.NodeID,
		trackNames: trackNames,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetConnectionHandler sets the connection handler for forwarding state updates
func (h *StateInputHandler) SetConnectionHandler(handler interface{}) {
	if ch, ok := handler.(interface {
		SetPosition(position common.Position)
		SetRotation(rotation common.Rotation)
	}); ok {
		h.connectionHandler = ch
		common.LogDebug("State input handler: ConnectionHandler set for node %s", h.nodeID)
	} else {
		common.LogError("State input handler: Invalid ConnectionHandler interface")
	}
}

// Start begins reading state objects from the track
func (h *StateInputHandler) Start() error {
	common.LogInfo("Starting state input handler for node %s", h.nodeID)

	go h.readLoop()

	return nil
}

// readLoop continuously reads MOQ objects from the track and forwards state updates
func (h *StateInputHandler) readLoop() {
	common.LogDebug("State input read loop started for node %s", h.nodeID)

	for {
		select {
		case <-h.ctx.Done():
			common.LogDebug("State input read loop stopped for node %s", h.nodeID)
			return
		default:
		}

		// Read the next object from the track
		obj, err := h.track.ReadObject(h.ctx)
		if err != nil {
			if err == io.EOF {
				common.LogInfo("State input track closed for node %s (EOF)", h.nodeID)
				return
			}
			if h.ctx.Err() != nil {
				// Context cancelled, normal shutdown
				return
			}
			common.LogError("Error reading state object for node %s: %v", h.nodeID, err)
			continue
		}

		// Process the object
		if err := h.processObject(obj); err != nil {
			common.LogError("Error processing state object for node %s: %v", h.nodeID, err)
			continue
		}

		h.objectsReceived++
	}
}

// processObject processes a single MOQ object containing NodeInfo3 state data
func (h *StateInputHandler) processObject(obj *moqtransport.Object) error {
	if obj == nil {
		return fmt.Errorf("received nil object")
	}

	// The payload is NodeInfo3 binary data (48 bytes)
	stateData := obj.Payload

	// Validate size
	if len(stateData) != 48 {
		return fmt.Errorf("invalid NodeInfo3 size: %d bytes (expected 48)", len(stateData))
	}

	h.bytesReceived += uint64(len(stateData))

	// Parse NodeInfo3 from bytes
	nodeInfo := inout.NodeInfo3FromBytes(stateData)

	// Log first few state updates for debugging
	if h.objectsReceived < 5 {
		common.LogDebug("State object received: node=%s, group=%d, object=%d, pos=(%.2f,%.2f,%.2f), rot=(%.2f,%.2f,%.2f)",
			h.nodeID, obj.GroupID, obj.ObjectID,
			nodeInfo.Position.X, nodeInfo.Position.Y, nodeInfo.Position.Z,
			nodeInfo.Rotation.Yaw, nodeInfo.Rotation.Pitch, nodeInfo.Rotation.Roll)
	}

	// Verify UUID matches (optional - for debugging)
	if nodeInfo.Uuid.String() != h.nodeID {
		common.LogWarn("State update UUID mismatch: expected %s, got %s", h.nodeID, nodeInfo.Uuid.String())
	}

	// Forward to ConnectionHandler if set
	if h.connectionHandler != nil {
		// Update position
		h.connectionHandler.SetPosition(nodeInfo.Position)
		h.lastPosition = &nodeInfo.Position

		// Update rotation
		h.connectionHandler.SetRotation(nodeInfo.Rotation)
		h.lastRotation = &nodeInfo.Rotation
	} else {
		// Store last known state for debugging
		h.lastPosition = &nodeInfo.Position
		h.lastRotation = &nodeInfo.Rotation

		if h.objectsReceived%100 == 0 {
			common.LogDebug("State input handler: received %d objects (no ConnectionHandler set)",
				h.objectsReceived)
		}
	}

	return nil
}

// Stop stops the state input handler
func (h *StateInputHandler) Stop() error {
	common.LogDebug("Stopping state input handler for node %s", h.nodeID)

	h.cancel()

	if h.track != nil {
		if err := h.track.Close(); err != nil {
			return fmt.Errorf("failed to close state input track: %w", err)
		}
	}

	common.LogInfo("State input handler stopped for node %s (received %d objects, %d bytes)",
		h.nodeID, h.objectsReceived, h.bytesReceived)

	return nil
}

// GetStats returns statistics about received state updates
func (h *StateInputHandler) GetStats() (objectsReceived uint64, bytesReceived uint64) {
	return h.objectsReceived, h.bytesReceived
}

// GetLastState returns the last known position and rotation
func (h *StateInputHandler) GetLastState() (position *common.Position, rotation *common.Rotation) {
	return h.lastPosition, h.lastRotation
}
