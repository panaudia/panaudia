package moq

import (
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// SpaceOutputHandler manages the per-connection MOQ output track for
// the "space" topic — the space-wide role-rule record (roles-muted /
// roles-kicked / roles-gain / roles-attenuation) written by
// space.role.* commands. Delivery is gated by the
// commands.ReadCapSpaceRead read scope at the writer
// (MoqDataWriter.sendSpace), not at this handler. Created only for
// connections holding the cap so the namespace is never announced to
// readers without permission. See plan/history/commands/space-read-path-plan.md.
type SpaceOutputHandler struct {
	moqSession       *moqtransport.Session
	trackNames       *TrackNames
	trackAdapter     *MoqTrackAdapter
	subscribeHandler *GenericOutputSubscribeHandler
}

// NewSpaceOutputHandler creates a new space output handler.
func NewSpaceOutputHandler(moqSession *moqtransport.Session, trackNames *TrackNames) (*SpaceOutputHandler, error) {
	adapter, err := NewMoqTrackAdapter(moqSession, trackNames.SpaceOutputNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create space output track adapter: %w", err)
	}

	subscribeHandler := NewGenericOutputSubscribeHandler(adapter, trackNames.SpaceOutputNamespace)

	return &SpaceOutputHandler{
		moqSession:       moqSession,
		trackNames:       trackNames,
		trackAdapter:     adapter,
		subscribeHandler: subscribeHandler,
	}, nil
}

// Start begins publishing to the space output track.
func (h *SpaceOutputHandler) Start() error {
	common.LogInfo("Starting space output handler for node %s", h.trackNames.NodeID)
	if err := h.trackAdapter.Start(); err != nil {
		return fmt.Errorf("failed to start space output track adapter: %w", err)
	}
	return nil
}

// GetTrackAdapter returns the track adapter for MoqDataWriter integration.
func (h *SpaceOutputHandler) GetTrackAdapter() *MoqTrackAdapter {
	return h.trackAdapter
}

// GetSubscribeHandler returns the subscribe handler for session integration.
func (h *SpaceOutputHandler) GetSubscribeHandler() moqtransport.SubscribeHandler {
	return h.subscribeHandler
}

// Stop stops publishing and cleans up resources.
func (h *SpaceOutputHandler) Stop() error {
	common.LogDebug("Stopping space output handler for node %s", h.trackNames.NodeID)
	if h.trackAdapter != nil {
		if err := h.trackAdapter.Stop(); err != nil {
			return fmt.Errorf("failed to stop space output track adapter: %w", err)
		}
	}
	stats := h.GetStats()
	common.LogInfo("Space output handler stopped. Published: %d objects, %d bytes, %d errors",
		stats.ObjectsPublished, stats.BytesPublished, stats.PublishErrors)
	return nil
}

// GetStats returns current publishing statistics.
func (h *SpaceOutputHandler) GetStats() OutputStats {
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
