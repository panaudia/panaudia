package moq

import (
	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// GenericOutputSubscribeHandler handles client subscriptions to a server output track.
// Reusable for state output, attributes output, etc.
type GenericOutputSubscribeHandler struct {
	trackAdapter      *MoqTrackAdapter
	expectedNamespace []string
}

// NewGenericOutputSubscribeHandler creates a new subscribe handler for an output track
func NewGenericOutputSubscribeHandler(trackAdapter *MoqTrackAdapter, expectedNamespace []string) *GenericOutputSubscribeHandler {
	return &GenericOutputSubscribeHandler{
		trackAdapter:      trackAdapter,
		expectedNamespace: expectedNamespace,
	}
}

// HandleSubscribe is called when a client subscribes to this output track
func (h *GenericOutputSubscribeHandler) HandleSubscribe(w *moqtransport.SubscribeResponseWriter, msg *moqtransport.SubscribeMessage) {
	common.LogInfo("Client subscribing to output track: %v", msg.Namespace)

	if !namespacesMatch(msg.Namespace, h.expectedNamespace) {
		common.LogWarn("Subscription to unexpected namespace: %v (expected %v)", msg.Namespace, h.expectedNamespace)
		w.Reject(ErrorCodeInternalError, "unexpected track namespace")
		return
	}

	if err := w.Accept(); err != nil {
		common.LogError("Failed to accept subscription: %v", err)
		return
	}

	h.trackAdapter.AddPublisher(w)
	common.LogDebug("Client successfully subscribed to output track: %v", h.expectedNamespace)
}
