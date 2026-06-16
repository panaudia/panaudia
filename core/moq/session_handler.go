package moq

import (
	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// SessionHandler handles generic MOQ messages (ANNOUNCE, SUBSCRIBE_ANNOUNCES, etc.)
// for a specific session
type SessionHandler struct {
	session *MoqSession
}

// NewSessionHandler creates a new session handler
func NewSessionHandler(session *MoqSession) *SessionHandler {
	return &SessionHandler{
		session: session,
	}
}

// Handle implements moqtransport.Handler
func (h *SessionHandler) Handle(rw moqtransport.ResponseWriter, msg *moqtransport.Message) {
	if msg == nil {
		common.LogDebug("SessionHandler received nil message")
		return
	}

	common.LogDebug("SessionHandler received message: %s", msg.Method)

	switch msg.Method {
	case moqtransport.MessageAnnounce:
		h.handleAnnounce(rw, msg)
	case moqtransport.MessageSubscribeAnnounces:
		h.handleSubscribeAnnounces(rw, msg)
	case moqtransport.MessageUnannounce:
		h.handleUnannounce(msg)
	case moqtransport.MessageAnnounceCancel:
		h.handleAnnounceCancel(msg)
	case moqtransport.MessageUnsubscribeAnnounces:
		h.handleUnsubscribeAnnounces(msg)
	case moqtransport.MessageGoAway:
		h.handleGoAway(msg)
	default:
		common.LogDebug("Unhandled message type: %s", msg.Method)
		if rw != nil {
			rw.Reject(0, "unhandled message type")
		}
	}
}

// handleAnnounce handles ANNOUNCE messages from clients
// Clients announce their input tracks so the server can subscribe to them
func (h *SessionHandler) handleAnnounce(rw moqtransport.ResponseWriter, msg *moqtransport.Message) {
	common.LogInfo("Received ANNOUNCE from client: namespace=%v", msg.Namespace)

	if rw == nil {
		common.LogError("ANNOUNCE handler received nil ResponseWriter")
		return
	}

	// Accept the announcement - the client is telling us about a track it will publish
	// The subscription manager already handles subscribing to expected tracks
	if err := rw.Accept(); err != nil {
		common.LogError("Failed to accept ANNOUNCE: %v", err)
		return
	}

	common.LogInfo("Accepted ANNOUNCE for namespace: %v", msg.Namespace)
}

// handleSubscribeAnnounces handles SUBSCRIBE_ANNOUNCES messages from clients
// Clients may want to know about tracks we announce
func (h *SessionHandler) handleSubscribeAnnounces(rw moqtransport.ResponseWriter, msg *moqtransport.Message) {
	common.LogInfo("Received SUBSCRIBE_ANNOUNCES from client: prefix=%v", msg.Namespace)

	if rw == nil {
		common.LogError("SUBSCRIBE_ANNOUNCES handler received nil ResponseWriter")
		return
	}

	// Accept the subscription to announcements
	if err := rw.Accept(); err != nil {
		common.LogError("Failed to accept SUBSCRIBE_ANNOUNCES: %v", err)
		return
	}

	common.LogInfo("Accepted SUBSCRIBE_ANNOUNCES for prefix: %v", msg.Namespace)
}

// handleUnannounce handles UNANNOUNCE messages
func (h *SessionHandler) handleUnannounce(msg *moqtransport.Message) {
	common.LogInfo("Received UNANNOUNCE: namespace=%v", msg.Namespace)
	// Clean up any state associated with this announcement
}

// handleAnnounceCancel handles ANNOUNCE_CANCEL messages
func (h *SessionHandler) handleAnnounceCancel(msg *moqtransport.Message) {
	common.LogInfo("Received ANNOUNCE_CANCEL: namespace=%v, error=%d, reason=%s",
		msg.Namespace, msg.ErrorCode, msg.ReasonPhrase)
}

// handleUnsubscribeAnnounces handles UNSUBSCRIBE_ANNOUNCES messages
func (h *SessionHandler) handleUnsubscribeAnnounces(msg *moqtransport.Message) {
	common.LogInfo("Received UNSUBSCRIBE_ANNOUNCES: prefix=%v", msg.Namespace)
}

// handleGoAway handles GO_AWAY messages. Under the funnel
// (plan/history/state-cleanup §3a) this is a Kill cause, never cleanup: sever
// the transport and the owner goroutine runs the full departure.
// Session migration to NewSessionURI is not supported.
func (h *SessionHandler) handleGoAway(msg *moqtransport.Message) {
	common.LogInfo("Received GO_AWAY: newSessionURI=%s", msg.NewSessionURI)
	if h.session != nil && h.session.closeTransport != nil {
		h.session.closeTransport("client GO_AWAY")
	}
}
