package moq

import (
	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// AudioOutputSubscribeHandler handles client subscriptions to the audio output track
type AudioOutputSubscribeHandler struct {
	trackAdapter *MoqTrackAdapter
	trackNames   *TrackNames
}

// NewAudioOutputSubscribeHandler creates a new subscribe handler for the audio output track
func NewAudioOutputSubscribeHandler(trackAdapter *MoqTrackAdapter, trackNames *TrackNames) *AudioOutputSubscribeHandler {
	return &AudioOutputSubscribeHandler{
		trackAdapter: trackAdapter,
		trackNames:   trackNames,
	}
}

// HandleSubscribe is called when a client subscribes to the audio output track
// It accepts the subscription and adds the publisher to the track adapter
func (h *AudioOutputSubscribeHandler) HandleSubscribe(w *moqtransport.SubscribeResponseWriter, msg *moqtransport.SubscribeMessage) {
	common.LogInfo("Client subscribing to audio output track: %v", msg.Namespace)

	// Verify this is for our output track
	if !namespacesMatch(msg.Namespace, h.trackNames.AudioOutputNamespace) {
		common.LogWarn("Subscription to unexpected namespace: %v (expected %v)", msg.Namespace, h.trackNames.AudioOutputNamespace)
		w.Reject(ErrorCodeInternalError, "unexpected track namespace")
		return
	}

	// Accept the subscription
	if err := w.Accept(); err != nil {
		common.LogError("Failed to accept subscription: %v", err)
		return
	}

	// Add this publisher to the track adapter
	h.trackAdapter.AddPublisher(w)

	common.LogDebug("Client successfully subscribed to audio output track for node %s", h.trackNames.NodeID)
}

// namespacesMatch checks if two namespaces are equal
func namespacesMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
