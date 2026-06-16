package moq

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/panaudia_server"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/quic-go/quic-go"
)

// Admission failures surfaced to the SUBSCRIBE rejection path. Distinct
// messages so the client can tell a retryable duplicate (old session
// still tearing down) from a full server.
var (
	errDuplicateSession = errors.New("a session for this identity is already active")
	errServerFull       = errors.New("server is full")
)

func nodeCreationError(serr *common.ServerError) error {
	if serr == nil {
		return errors.New("node creation failed")
	}
	switch serr.Code {
	case common.SERVER_ERROR_DUPLICATE:
		return errDuplicateSession
	case common.SERVER_ERROR_FULL:
		return errServerFull
	default:
		return fmt.Errorf("node creation failed: %s", serr.Message)
	}
}

// isDisconnectError returns true if the error is expected during client disconnection
func isDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "canceled by remote") ||
		strings.Contains(msg, "unknown announcement") ||
		strings.Contains(msg, "Context was terminated")
}

// logCloseError logs at DEBUG level if the error is expected during disconnect, ERROR otherwise
func logCloseError(format string, err error, args ...any) {
	allArgs := append([]any{}, args...)
	allArgs = append(allArgs, err)
	if isDisconnectError(err) {
		common.LogDebug(format, allArgs...)
	} else {
		common.LogError(format, allArgs...)
	}
}

// MoqSession represents a single MOQ client session
type MoqSession struct {
	conn       *quic.Conn
	moqSession *moqtransport.Session
	server     *MoqServer

	// Transport handle for the session registry (plan/history/state-cleanup
	// phases 2/3): transportCtx is done when the underlying WT session /
	// QUIC connection is gone; closeTransport force-closes it (the Kill
	// path). Set by the connection handlers at construction.
	transportCtx   context.Context
	closeTransport func(reason string)
	transportName  string // "moq-wt" | "moq-quic"

	// URL query parameters from the WebTransport upgrade request.
	// Used to pass extra connection attributes (same as WebRTC query params).
	queryParams map[string][]string

	// Authenticated node configuration from JWT (set when first track is subscribed)
	nodeConfig *common.NodeConfig

	// Track names for this session
	trackNames *TrackNames

	// Subscription manager for handling track announcements and subscriptions
	subscriptionMgr *SubscriptionManager

	// ConnectionHandler integrates with the mixing backend
	connectionHandler panaudia_server.ConnectionHandler
}

// SetNodeConfig sets the authenticated node configuration for this session
// and builds the per-session machinery. On error the session is rolled
// back to its pre-call state so the caller can Reject the SUBSCRIBE (and a
// client retry re-runs cleanly) instead of leaving the session
// half-admitted — authenticated with no node and no audio
// (plan/history/state-cleanup/findings.md §2.3).
func (s *MoqSession) SetNodeConfig(config common.NodeConfig) error {
	// MOQ clients send mono Opus (1 channel), unlike WebRTC which sends stereo
	config.InputChannels = 1

	s.nodeConfig = &config
	common.LogInfo("Node config set for session: %s (%s)", config.Name, config.Uuid)

	fail := func(err error) error {
		if s.subscriptionMgr != nil {
			if closeErr := s.subscriptionMgr.Close(); closeErr != nil {
				logCloseError("Error closing subscription manager after failed setup: %v", closeErr)
			}
		}
		s.subscriptionMgr = nil
		s.trackNames = nil
		s.nodeConfig = nil
		return err
	}

	// Generate track names from the node config
	s.trackNames = &TrackNames{}
	*s.trackNames = GenerateTrackNames(config)

	// Create and start subscription manager
	s.subscriptionMgr = NewSubscriptionManager(s, s.trackNames)
	if err := s.subscriptionMgr.Start(); err != nil {
		common.LogError("Failed to start subscription manager: %v", err)
		return fail(fmt.Errorf("subscription manager start failed: %w", err))
	}

	// Get the MOQ output track adapter from subscription manager
	moqOutputAdapter := s.subscriptionMgr.GetAudioOutputAdapter()
	if moqOutputAdapter == nil {
		common.LogError("Failed to get audio output adapter from subscription manager")
		return fail(fmt.Errorf("no audio output adapter"))
	}

	// Create ConnectionHandler using the backend factory, handing it the
	// transport's session handle: the backend owns registration with the
	// session registry and the departure funnel (phase 3).
	live := &sessions.FuncSession{
		AliveFn: func() bool { return s.transportCtx == nil || s.transportCtx.Err() == nil },
		KillFn: func(reason string) {
			common.LogInfo("Killing MOQ session %s: %s", config.Uuid, reason)
			if s.closeTransport != nil {
				s.closeTransport(reason)
			}
		},
	}
	var serr *common.ServerError
	s.connectionHandler, serr = panaudia_server.NewConnectionHandlerE(s.server.backend, config, moqOutputAdapter, live, s.transportName)
	if s.connectionHandler == nil {
		common.LogWarn("Failed to create ConnectionHandler for node %s: %v", config.Uuid, serr)
		return fail(nodeCreationError(serr))
	}

	common.LogInfo("ConnectionHandler created for node %s", config.Uuid)

	// Wire MOQ data writer so the backend can send
	// state/attributes/entity/space to clients. The space adapter is
	// nil for connections without the commands.ReadCapSpaceRead cap
	// (handler is skipped at SubscriptionManager.Start in that case).
	stateOutputAdapter := s.subscriptionMgr.GetStateOutputAdapter()
	attributesOutputAdapter := s.subscriptionMgr.GetAttributesOutputAdapter()
	entityOutputAdapter := s.subscriptionMgr.GetEntityOutputAdapter()
	spaceOutputAdapter := s.subscriptionMgr.GetSpaceOutputAdapter()
	if stateOutputAdapter != nil || attributesOutputAdapter != nil || entityOutputAdapter != nil || spaceOutputAdapter != nil {
		moqWriter := NewMoqDataWriter(stateOutputAdapter, attributesOutputAdapter, entityOutputAdapter, spaceOutputAdapter, config.Uuid, config.SubSpaces, config.ReadCaps)
		s.connectionHandler.SetReceiveSender(moqWriter)
		common.LogInfo("MoqDataWriter wired for node %s (state=%v, attributes=%v, entity=%v, space=%v)",
			config.Uuid, stateOutputAdapter != nil, attributesOutputAdapter != nil, entityOutputAdapter != nil, spaceOutputAdapter != nil)
	}

	// Wire the input handlers to the ConnectionHandler
	s.subscriptionMgr.SetConnectionHandler(s.connectionHandler)

	// Connect the handler to activate it in the mixing backend.
	// Run in a goroutine because Connect() may block (e.g. the cloud-mixer's
	// MixerClient.Connect runs a blocking monitor loop). This matches how the
	// WebRTC path calls Connect() in a goroutine.
	go func() {
		if err := s.connectionHandler.Connect(); err != nil {
			// server-fragilities #4: don't leave the session looking
			// active with no backend — sever the transport (Kill cause)
			// and the owner goroutine runs the departure; the client
			// sees the close and can retry.
			common.LogError("Failed to connect ConnectionHandler for node %s: %v — killing session", config.Uuid, err)
			live.Kill("backend connect failed")
		}
	}()

	common.LogInfo("MOQ session fully initialized for node %s (%s)", config.Name, config.Uuid)
	return nil
}

// GetNodeConfig returns the authenticated node configuration (if set)
func (s *MoqSession) GetNodeConfig() *common.NodeConfig {
	return s.nodeConfig
}

// Close cleans up the session and associated resources
func (s *MoqSession) Close() error {
	if s.nodeConfig != nil {
		common.LogInfo("Closing MOQ session for node %s (%s)", s.nodeConfig.Name, s.nodeConfig.Uuid)
	} else {
		common.LogDebug("Closing unauthenticated MOQ session")
	}

	var lastErr error

	// Stop ConnectionHandler first. Stop() enqueues a NODE_CHANGE_DELETE
	// on the space's changesQueue and the mixer's tick loop processes it
	// — that path eventually calls backend.FreeSource on the mixer's own
	// goroutine, which is the only goroutine safe to free the encoder's
	// cgo state from (calling it from this goroutine while the mixer is
	// mid-tick in opus_encode causes a SIGSEGV in cgo). The WebRTC
	// disconnect path uses the same single-call pattern.
	if s.connectionHandler != nil {
		s.connectionHandler.Stop()
		common.LogDebug("ConnectionHandler stopped")
	}

	// Close subscription manager (stops track handlers)
	if s.subscriptionMgr != nil {
		if err := s.subscriptionMgr.Close(); err != nil {
			logCloseError("Error closing subscription manager: %v", err)
			lastErr = err
		}
	}

	// Close the MOQ session (closes tracks)
	// Use recover to handle panics from moqtransport if session wasn't fully initialized
	if s.moqSession != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					common.LogDebug("Recovered from panic closing MOQ session (session may not have been fully initialized): %v", r)
				}
			}()
			if err := s.moqSession.Close(); err != nil {
				logCloseError("Error closing MOQ transport session: %v", err)
				lastErr = err
			}
		}()
	}

	// Note: QUIC connection is closed by handleConnection defer, not here
	// This prevents double-close errors.
	// Registry teardown is owned by DepartNode (via connectionHandler.Stop
	// above) since phase 3.

	if s.nodeConfig != nil {
		common.LogInfo("MOQ session closed for node %s", s.nodeConfig.Uuid)
	}

	return lastErr
}
