package moq

import (
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// AttributesOutputHandler manages the attributes output track for publishing
// participant attributes (JSON) to clients
type AttributesOutputHandler struct {
	moqSession       *moqtransport.Session
	trackNames       *TrackNames
	trackAdapter     *MoqTrackAdapter
	subscribeHandler *GenericOutputSubscribeHandler
}

// NewAttributesOutputHandler creates a new attributes output handler
func NewAttributesOutputHandler(moqSession *moqtransport.Session, trackNames *TrackNames) (*AttributesOutputHandler, error) {
	adapter, err := NewMoqTrackAdapter(moqSession, trackNames.AttributesOutputNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create attributes output track adapter: %w", err)
	}

	subscribeHandler := NewGenericOutputSubscribeHandler(adapter, trackNames.AttributesOutputNamespace)

	return &AttributesOutputHandler{
		moqSession:       moqSession,
		trackNames:       trackNames,
		trackAdapter:     adapter,
		subscribeHandler: subscribeHandler,
	}, nil
}

// Start begins publishing to the attributes output track
func (h *AttributesOutputHandler) Start() error {
	common.LogInfo("Starting attributes output handler for node %s", h.trackNames.NodeID)
	if err := h.trackAdapter.Start(); err != nil {
		return fmt.Errorf("failed to start attributes output track adapter: %w", err)
	}
	return nil
}

// GetTrackAdapter returns the track adapter for MoqDataWriter integration
func (h *AttributesOutputHandler) GetTrackAdapter() *MoqTrackAdapter {
	return h.trackAdapter
}

// GetSubscribeHandler returns the subscribe handler for session integration
func (h *AttributesOutputHandler) GetSubscribeHandler() moqtransport.SubscribeHandler {
	return h.subscribeHandler
}

// Stop stops publishing and cleans up resources
func (h *AttributesOutputHandler) Stop() error {
	common.LogDebug("Stopping attributes output handler for node %s", h.trackNames.NodeID)
	if h.trackAdapter != nil {
		if err := h.trackAdapter.Stop(); err != nil {
			return fmt.Errorf("failed to stop attributes output track adapter: %w", err)
		}
	}
	stats := h.GetStats()
	common.LogInfo("Attributes output handler stopped. Published: %d objects, %d bytes, %d errors",
		stats.ObjectsPublished, stats.BytesPublished, stats.PublishErrors)
	return nil
}

// GetStats returns current publishing statistics
func (h *AttributesOutputHandler) GetStats() OutputStats {
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
