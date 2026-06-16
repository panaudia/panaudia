package moq

import (
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// StateOutputHandler manages the state output track for publishing
// participant state (NodeInfo3) to clients
type StateOutputHandler struct {
	moqSession       *moqtransport.Session
	trackNames       *TrackNames
	trackAdapter     *MoqTrackAdapter
	subscribeHandler *GenericOutputSubscribeHandler
}

// NewStateOutputHandler creates a new state output handler
func NewStateOutputHandler(moqSession *moqtransport.Session, trackNames *TrackNames) (*StateOutputHandler, error) {
	adapter, err := NewMoqTrackAdapter(moqSession, trackNames.StateOutputNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create state output track adapter: %w", err)
	}

	subscribeHandler := NewGenericOutputSubscribeHandler(adapter, trackNames.StateOutputNamespace)

	return &StateOutputHandler{
		moqSession:       moqSession,
		trackNames:       trackNames,
		trackAdapter:     adapter,
		subscribeHandler: subscribeHandler,
	}, nil
}

// Start begins publishing to the state output track
func (h *StateOutputHandler) Start() error {
	common.LogInfo("Starting state output handler for node %s", h.trackNames.NodeID)
	if err := h.trackAdapter.Start(); err != nil {
		return fmt.Errorf("failed to start state output track adapter: %w", err)
	}
	return nil
}

// GetTrackAdapter returns the track adapter for MoqDataWriter integration
func (h *StateOutputHandler) GetTrackAdapter() *MoqTrackAdapter {
	return h.trackAdapter
}

// GetSubscribeHandler returns the subscribe handler for session integration
func (h *StateOutputHandler) GetSubscribeHandler() moqtransport.SubscribeHandler {
	return h.subscribeHandler
}

// Stop stops publishing and cleans up resources
func (h *StateOutputHandler) Stop() error {
	common.LogDebug("Stopping state output handler for node %s", h.trackNames.NodeID)
	if h.trackAdapter != nil {
		if err := h.trackAdapter.Stop(); err != nil {
			return fmt.Errorf("failed to stop state output track adapter: %w", err)
		}
	}
	stats := h.GetStats()
	common.LogInfo("State output handler stopped. Published: %d objects, %d bytes, %d errors",
		stats.ObjectsPublished, stats.BytesPublished, stats.PublishErrors)
	return nil
}

// OutputStats contains statistics about published data
type OutputStats struct {
	ObjectsPublished uint64
	BytesPublished   uint64
	PublishErrors    uint64
}

// GetStats returns current publishing statistics
func (h *StateOutputHandler) GetStats() OutputStats {
	if h.trackAdapter == nil {
		return OutputStats{}
	}
	objectsPublished, bytesPublished, publishErrors := h.trackAdapter.GetStats()
	return OutputStats{
		ObjectsPublished: objectsPublished,
		BytesPublished:   bytesPublished,
		PublishErrors:    publishErrors,
	}
}
