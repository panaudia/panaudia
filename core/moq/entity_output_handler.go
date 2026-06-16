package moq

import (
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// EntityOutputHandler manages the entity output track. Each subscriber
// receives only the entity ops whose keys pertain to itself (filter
// applied in MoqDataWriter.sendEntity), so this handler is a thin wrapper
// around a per-session track adapter.
type EntityOutputHandler struct {
	moqSession       *moqtransport.Session
	trackNames       *TrackNames
	trackAdapter     *MoqTrackAdapter
	subscribeHandler *GenericOutputSubscribeHandler
}

// NewEntityOutputHandler creates a new entity output handler.
func NewEntityOutputHandler(moqSession *moqtransport.Session, trackNames *TrackNames) (*EntityOutputHandler, error) {
	adapter, err := NewMoqTrackAdapter(moqSession, trackNames.EntityOutputNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create entity output track adapter: %w", err)
	}

	subscribeHandler := NewGenericOutputSubscribeHandler(adapter, trackNames.EntityOutputNamespace)

	return &EntityOutputHandler{
		moqSession:       moqSession,
		trackNames:       trackNames,
		trackAdapter:     adapter,
		subscribeHandler: subscribeHandler,
	}, nil
}

// Start begins publishing to the entity output track.
func (h *EntityOutputHandler) Start() error {
	common.LogInfo("Starting entity output handler for node %s", h.trackNames.NodeID)
	if err := h.trackAdapter.Start(); err != nil {
		return fmt.Errorf("failed to start entity output track adapter: %w", err)
	}
	return nil
}

// GetTrackAdapter returns the track adapter for MoqDataWriter integration.
func (h *EntityOutputHandler) GetTrackAdapter() *MoqTrackAdapter {
	return h.trackAdapter
}

// GetSubscribeHandler returns the subscribe handler for session integration.
func (h *EntityOutputHandler) GetSubscribeHandler() moqtransport.SubscribeHandler {
	return h.subscribeHandler
}

// Stop stops publishing and cleans up resources.
func (h *EntityOutputHandler) Stop() error {
	common.LogDebug("Stopping entity output handler for node %s", h.trackNames.NodeID)
	if h.trackAdapter != nil {
		if err := h.trackAdapter.Stop(); err != nil {
			return fmt.Errorf("failed to stop entity output track adapter: %w", err)
		}
	}
	stats := h.GetStats()
	common.LogInfo("Entity output handler stopped. Published: %d objects, %d bytes, %d errors",
		stats.ObjectsPublished, stats.BytesPublished, stats.PublishErrors)
	return nil
}

// GetStats returns current publishing statistics.
func (h *EntityOutputHandler) GetStats() OutputStats {
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
