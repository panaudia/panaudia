package moq

import (
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// AudioOutputHandler manages the audio output track for publishing mixed audio to clients
type AudioOutputHandler struct {
	moqSession       *moqtransport.Session
	trackNames       *TrackNames
	trackAdapter     *MoqTrackAdapter
	subscribeHandler *AudioOutputSubscribeHandler
}

// NewAudioOutputHandler creates a new audio output handler
func NewAudioOutputHandler(moqSession *moqtransport.Session, trackNames *TrackNames) (*AudioOutputHandler, error) {
	// Create the MOQ track adapter
	adapter, err := NewMoqTrackAdapter(moqSession, trackNames.AudioOutputNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create track adapter: %w", err)
	}

	// Create subscribe handler for the output track
	subscribeHandler := NewAudioOutputSubscribeHandler(adapter, trackNames)

	handler := &AudioOutputHandler{
		moqSession:       moqSession,
		trackNames:       trackNames,
		trackAdapter:     adapter,
		subscribeHandler: subscribeHandler,
	}

	return handler, nil
}

// Start begins publishing to the output track
func (h *AudioOutputHandler) Start() error {
	common.LogInfo("Starting audio output handler for node %s", h.trackNames.NodeID)

	if err := h.trackAdapter.Start(); err != nil {
		return fmt.Errorf("failed to start track adapter: %w", err)
	}

	common.LogDebug("Audio output handler started for namespace: %v", h.trackNames.AudioOutputNamespace)

	// The one-shot CLOCKTEST playout-drift instrumentation that lived here
	// was removed in the state-cleanup scurf pass (plan/history/state-cleanup
	// phase 7); recover it from commit a6ef33c if the drift investigation
	// resumes.

	return nil
}

// GetTrackAdapter returns the track adapter for use by ConnectionHandler
// ConnectionHandler will call WriteSample() on this adapter
func (h *AudioOutputHandler) GetTrackAdapter() *MoqTrackAdapter {
	return h.trackAdapter
}

// GetSubscribeHandler returns the subscribe handler for session integration
func (h *AudioOutputHandler) GetSubscribeHandler() moqtransport.SubscribeHandler {
	return h.subscribeHandler
}

// Stop stops publishing and cleans up resources
func (h *AudioOutputHandler) Stop() error {
	common.LogDebug("Stopping audio output handler for node %s", h.trackNames.NodeID)

	if h.trackAdapter != nil {
		if err := h.trackAdapter.Stop(); err != nil {
			return fmt.Errorf("failed to stop track adapter: %w", err)
		}
	}

	stats := h.GetStats()
	common.LogInfo("Audio output handler stopped. Published: %d objects, %d bytes, %d errors",
		stats.ObjectsPublished, stats.BytesPublished, stats.PublishErrors)

	return nil
}

// AudioOutputStats contains statistics about published audio
type AudioOutputStats struct {
	ObjectsPublished uint64
	BytesPublished   uint64
	PublishErrors    uint64
}

// GetStats returns current publishing statistics
func (h *AudioOutputHandler) GetStats() AudioOutputStats {
	if h.trackAdapter == nil {
		return AudioOutputStats{}
	}

	objectsPublished, bytesPublished, publishErrors := h.trackAdapter.GetStats()
	return AudioOutputStats{
		ObjectsPublished: objectsPublished,
		BytesPublished:   bytesPublished,
		PublishErrors:    publishErrors,
	}
}
