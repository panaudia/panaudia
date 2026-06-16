package moq

import (
	"context"
	"fmt"
	"io"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// AudioInputHandler handles incoming audio from a client's audio input track
type AudioInputHandler struct {
	track      *moqtransport.RemoteTrack
	nodeID     string
	trackNames *TrackNames

	// ConnectionHandler interface (will be set in Phase 7)
	// For now we use interface{} to avoid circular dependencies
	connectionHandler interface {
		WriteOpus(src []byte) error
	}

	ctx    context.Context
	cancel context.CancelFunc

	// Stats
	objectsReceived uint64
	bytesReceived   uint64
}

// NewAudioInputHandler creates a new audio input handler
func NewAudioInputHandler(track *moqtransport.RemoteTrack, trackNames *TrackNames) *AudioInputHandler {
	ctx, cancel := context.WithCancel(context.Background())

	return &AudioInputHandler{
		track:      track,
		nodeID:     trackNames.NodeID,
		trackNames: trackNames,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetConnectionHandler sets the connection handler for forwarding audio
func (h *AudioInputHandler) SetConnectionHandler(handler interface{}) {
	if ch, ok := handler.(interface{ WriteOpus(src []byte) error }); ok {
		h.connectionHandler = ch
		common.LogDebug("Audio input handler: ConnectionHandler set for node %s", h.nodeID)
	} else {
		common.LogError("Audio input handler: Invalid ConnectionHandler interface")
	}
}

// Start begins reading audio objects from the track
func (h *AudioInputHandler) Start() error {
	common.LogInfo("Starting audio input handler for node %s", h.nodeID)

	go h.readLoop()

	return nil
}

// readLoop continuously reads MOQ objects from the track and forwards Opus data
func (h *AudioInputHandler) readLoop() {
	common.LogDebug("Audio input read loop started for node %s", h.nodeID)

	for {
		select {
		case <-h.ctx.Done():
			common.LogDebug("Audio input read loop stopped for node %s", h.nodeID)
			return
		default:
		}

		// Read the next object from the track
		obj, err := h.track.ReadObject(h.ctx)
		if err != nil {
			if err == io.EOF {
				common.LogInfo("Audio input track closed for node %s (EOF)", h.nodeID)
				return
			}
			if h.ctx.Err() != nil {
				// Context cancelled, normal shutdown
				return
			}
			common.LogError("Error reading audio object for node %s: %v", h.nodeID, err)
			continue
		}

		// Process the object
		if err := h.processObject(obj); err != nil {
			common.LogError("Error processing audio object for node %s: %v", h.nodeID, err)
			continue
		}

		h.objectsReceived++
	}
}

// processObject processes a single MOQ object containing Opus audio data
func (h *AudioInputHandler) processObject(obj *moqtransport.Object) error {
	if obj == nil {
		return fmt.Errorf("received nil object")
	}

	// The payload is raw Opus frame bytes
	opusData := obj.Payload

	if len(opusData) == 0 {
		common.LogWarn("Received empty Opus frame for node %s (Group=%d, Object=%d)",
			h.nodeID, obj.GroupID, obj.ObjectID)
		return nil
	}

	// Validate Opus frame size: minimum 3 bytes (Opus DTX/silence frames),
	// typical 60-200 bytes for 20ms voice, up to ~1000 for high-bitrate music
	if len(opusData) < 3 || len(opusData) > 1000 {
		return fmt.Errorf("invalid Opus frame size: %d bytes (expected 3-1000)", len(opusData))
	}

	h.bytesReceived += uint64(len(opusData))

	// Log first few frames for debugging
	if h.objectsReceived < 5 {
		common.LogDebug("Audio object received: node=%s, group=%d, object=%d, size=%d bytes",
			h.nodeID, obj.GroupID, obj.ObjectID, len(opusData))
	}

	// Forward to ConnectionHandler if set
	if h.connectionHandler != nil {
		if err := h.connectionHandler.WriteOpus(opusData); err != nil {
			return fmt.Errorf("failed to write Opus to ConnectionHandler: %w", err)
		}
	} else {
		// During testing or before ConnectionHandler is set, just count the data
		if h.objectsReceived%100 == 0 {
			common.LogDebug("Audio input handler: received %d objects, %d bytes (no ConnectionHandler set)",
				h.objectsReceived, h.bytesReceived)
		}
	}

	return nil
}

// Stop stops the audio input handler
func (h *AudioInputHandler) Stop() error {
	common.LogDebug("Stopping audio input handler for node %s", h.nodeID)

	h.cancel()

	if h.track != nil {
		if err := h.track.Close(); err != nil {
			return fmt.Errorf("failed to close audio input track: %w", err)
		}
	}

	common.LogInfo("Audio input handler stopped for node %s (received %d objects, %d bytes)",
		h.nodeID, h.objectsReceived, h.bytesReceived)

	return nil
}

// GetStats returns statistics about received audio
func (h *AudioInputHandler) GetStats() (objectsReceived uint64, bytesReceived uint64) {
	return h.objectsReceived, h.bytesReceived
}
