package moq

import (
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
)

// KVP parameter key for resume opID (odd = length-prefixed bytes)
const kvpKeyResumeOpID = 0xFF01

// MOQ error codes (using moqtransport.ErrorCodeSubscribe type)
const (
	ErrorCodeUnauthorized  moqtransport.ErrorCodeSubscribe = moqtransport.ErrorCodeSubscribeUnauthorized
	ErrorCodeInternalError moqtransport.ErrorCodeSubscribe = moqtransport.ErrorCodeSubscribeInternal
)

// SessionSubscribeHandler handles SUBSCRIBE messages for a specific session
// and performs authentication
type SessionSubscribeHandler struct {
	session    *MoqSession
	authoriser Authoriser
	// When true, SUBSCRIBE messages without an Authorization parameter are
	// authenticated via AuthoriseWithoutTicket using only the session's
	// WebTransport query parameters (uuid, name, etc).
	unticketed bool

	// Track whether this session has been authenticated
	authenticated bool
}

// NewSessionSubscribeHandler creates a new subscribe handler for a session.
// Pass unticketed=true to let ticketless clients authenticate via URL query
// parameters alone (mirrors the WebRTC PANAUDIA_UNTICKETED flow).
func NewSessionSubscribeHandler(session *MoqSession, authoriser Authoriser, unticketed bool) *SessionSubscribeHandler {
	return &SessionSubscribeHandler{
		session:       session,
		authoriser:    authoriser,
		unticketed:    unticketed,
		authenticated: false,
	}
}

// HandleSubscribe implements moqtransport.SubscribeHandler
func (h *SessionSubscribeHandler) HandleSubscribe(w *moqtransport.SubscribeResponseWriter, msg *moqtransport.SubscribeMessage) {
	common.LogDebug("Session SUBSCRIBE handler: track %v/%s", msg.Namespace, msg.Track)
	common.LogDebug("SUBSCRIBE msg details - RequestID: %d, TrackAlias: %d, Auth len: %d",
		msg.RequestID, msg.TrackAlias, len(msg.Authorization))
	if msg.Authorization != "" {
		common.LogDebug("Authorization field present (first 50 chars): %.50s...", msg.Authorization)
	} else {
		common.LogDebug("Authorization field is EMPTY")
	}

	// If not yet authenticated, authenticate from the Authorization field
	// (or, if the server is running unticketed, from the URL query params).
	if !h.authenticated {
		var nodeConfig common.NodeConfig
		var err error

		if msg.Authorization == "" {
			if !h.unticketed {
				common.LogWarn("No authorization token in SUBSCRIBE message")
				w.Reject(ErrorCodeUnauthorized, "authentication required")
				return
			}
			common.LogDebug("Unticketed mode: authenticating via query params")
			nodeConfig, err = h.authenticateWithoutTicket()
		} else {
			common.LogDebug("Authenticating session with token (length: %d)", len(msg.Authorization))
			nodeConfig, err = h.authenticateToken(msg.Authorization)
		}

		if err != nil {
			common.LogError("Authentication failed: %v", err)
			w.Reject(ErrorCodeUnauthorized, fmt.Sprintf("authentication failed: %v", err))
			return
		}

		// Extract resume opID from parameters if provided
		if kvp, ok := msg.Parameters.GetParameter(kvpKeyResumeOpID); ok && len(kvp.ValueBytes) == 8 {
			nodeConfig.ResumeOpID = binary.BigEndian.Uint64(kvp.ValueBytes)
			common.LogDebug("Resume opID from SUBSCRIBE parameters: %d", nodeConfig.ResumeOpID)
		}

		// Store the node config in the session and build the per-session
		// machinery. Failure (duplicate identity, server full, internal
		// error) rejects the SUBSCRIBE instead of half-admitting the
		// client; SetNodeConfig rolled the session back, so a retry
		// re-runs this block cleanly.
		if err := h.session.SetNodeConfig(nodeConfig); err != nil {
			common.LogWarn("Session setup failed for %s (%s): %v", nodeConfig.Name, nodeConfig.Uuid, err)
			w.Reject(ErrorCodeInternalError, err.Error())
			return
		}
		h.authenticated = true

		common.LogInfo("Session authenticated: %s (%s)", nodeConfig.Name, nodeConfig.Uuid)
	}

	// Delegate to the appropriate track handler based on the namespace
	if h.session.subscriptionMgr != nil && h.session.trackNames != nil {
		trackNames := h.session.trackNames

		// Audio output track
		if audioAdapter := h.session.subscriptionMgr.GetAudioOutputAdapter(); audioAdapter != nil {
			if namespacesMatch(msg.Namespace, trackNames.AudioOutputNamespace) {
				common.LogDebug("Delegating to audio output subscribe handler")
				if err := w.Accept(); err != nil {
					common.LogError("Failed to accept subscription: %v", err)
					return
				}
				audioAdapter.AddPublisher(w)
				common.LogDebug("Client successfully subscribed to audio output track")
				return
			}
		}

		// State output track
		if stateAdapter := h.session.subscriptionMgr.GetStateOutputAdapter(); stateAdapter != nil {
			if namespacesMatch(msg.Namespace, trackNames.StateOutputNamespace) {
				common.LogDebug("Delegating to state output subscribe handler")
				if err := w.Accept(); err != nil {
					common.LogError("Failed to accept subscription: %v", err)
					return
				}
				stateAdapter.AddPublisher(w)
				common.LogDebug("Client successfully subscribed to state output track")
				return
			}
		}

		// Attributes output track
		if attrsAdapter := h.session.subscriptionMgr.GetAttributesOutputAdapter(); attrsAdapter != nil {
			if namespacesMatch(msg.Namespace, trackNames.AttributesOutputNamespace) {
				common.LogDebug("Delegating to attributes output subscribe handler")
				if err := w.Accept(); err != nil {
					common.LogError("Failed to accept subscription: %v", err)
					return
				}
				attrsAdapter.AddPublisher(w)
				common.LogDebug("Client successfully subscribed to attributes output track")
				return
			}
		}

		// Entity output track (per-client filtered)
		if entityAdapter := h.session.subscriptionMgr.GetEntityOutputAdapter(); entityAdapter != nil {
			if namespacesMatch(msg.Namespace, trackNames.EntityOutputNamespace) {
				common.LogDebug("Delegating to entity output subscribe handler")
				if err := w.Accept(); err != nil {
					common.LogError("Failed to accept subscription: %v", err)
					return
				}
				entityAdapter.AddPublisher(w)
				common.LogDebug("Client successfully subscribed to entity output track")
				return
			}
		}

		// Space output track (gated on the commands.ReadCapSpaceRead
		// cap — the adapter is nil for holders without the cap, so a
		// SUBSCRIBE for this namespace from an unauthorised holder
		// just falls through to the catch-all Accept below).
		if spaceAdapter := h.session.subscriptionMgr.GetSpaceOutputAdapter(); spaceAdapter != nil {
			if namespacesMatch(msg.Namespace, trackNames.SpaceOutputNamespace) {
				common.LogDebug("Delegating to space output subscribe handler")
				if err := w.Accept(); err != nil {
					common.LogError("Failed to accept subscription: %v", err)
					return
				}
				spaceAdapter.AddPublisher(w)
				common.LogDebug("Client successfully subscribed to space output track")
				return
			}
		}
	}

	// For other tracks (input tracks), just accept them
	// The subscription manager will handle the actual track setup via SubscribeAnnouncements
	common.LogDebug("Accepting subscription to %v/%s", msg.Namespace, msg.Track)
	if err := w.Accept(); err != nil {
		common.LogError("Failed to accept subscription: %v", err)
	}
}

// authenticateWithoutTicket builds a NodeConfig from the session's URL query
// parameters alone (no JWT). Used when the server runs with Unticketed=true
// and the client sent a SUBSCRIBE without an Authorization parameter.
func (h *SessionSubscribeHandler) authenticateWithoutTicket() (common.NodeConfig, error) {
	queryMap := make(map[string][]string)
	if h.session.queryParams != nil {
		for k, v := range h.session.queryParams {
			queryMap[k] = v
		}
	}

	nodeConfig, err := h.authoriser.AuthoriseWithoutTicket(queryMap)
	if err != nil {
		return common.NodeConfig{}, fmt.Errorf("unticketed authorisation failed: %w", err)
	}

	if err := validateNodeConfig(nodeConfig); err != nil {
		return common.NodeConfig{}, fmt.Errorf("node config validation failed: %w", err)
	}

	return nodeConfig, nil
}

// authenticateToken validates a JWT token and returns the NodeConfig.
// It merges any URL query parameters from the WebTransport upgrade request
// so that extra connection attributes (position, name, etc.) are passed
// through to the Authoriser — matching the WebRTC query-string behaviour.
func (h *SessionSubscribeHandler) authenticateToken(token string) (common.NodeConfig, error) {
	// Start with URL query params from the WebTransport upgrade request (if any)
	queryMap := make(map[string][]string)
	if h.session.queryParams != nil {
		for k, v := range h.session.queryParams {
			queryMap[k] = v
		}
	}

	// Set/override the ticket with the JWT from the SUBSCRIBE Authorization field
	queryMap["ticket"] = []string{token}

	// Use the existing DirectAuthoriser to validate the JWT and extract NodeConfig
	nodeConfig, err := h.authoriser.Authorise(queryMap)
	if err != nil {
		return common.NodeConfig{}, fmt.Errorf("token validation failed: %w", err)
	}

	// Validate the node config
	if err := validateNodeConfig(nodeConfig); err != nil {
		return common.NodeConfig{}, fmt.Errorf("node config validation failed: %w", err)
	}

	return nodeConfig, nil
}

// validateNodeConfig performs validation on the NodeConfig
func validateNodeConfig(config common.NodeConfig) error {
	// UUID must be set
	if config.Uuid.String() == "00000000-0000-0000-0000-000000000000" {
		return fmt.Errorf("invalid node config: UUID not set")
	}

	// Validate gain bounds
	if config.Gain < 0.0 || config.Gain > 3.0 {
		return fmt.Errorf("invalid gain value: %f (must be 0.0-3.0)", config.Gain)
	}

	// Validate attenuation bounds
	if config.Attenuation < 0.0 || config.Attenuation > 3.0 {
		return fmt.Errorf("invalid attenuation value: %f (must be 0.0-3.0)", config.Attenuation)
	}

	return nil
}
