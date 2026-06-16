package moq

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/commands"
	"github.com/panaudia/panaudia/core/common"
)

// SubscriptionManager handles track subscriptions and announcements for a session
type SubscriptionManager struct {
	session    *MoqSession
	moqSession *moqtransport.Session
	tracks     *TrackNames

	// Subscribed client tracks
	audioInputTrack   *moqtransport.RemoteTrack
	stateInputTrack   *moqtransport.RemoteTrack
	controlInputTrack *moqtransport.RemoteTrack

	// Track handlers
	audioInputHandler       *AudioInputHandler
	stateInputHandler       *StateInputHandler
	controlInputHandler     *ControlInputHandler
	audioOutputHandler      *AudioOutputHandler
	stateOutputHandler      *StateOutputHandler
	attributesOutputHandler *AttributesOutputHandler
	entityOutputHandler     *EntityOutputHandler
	// spaceOutputHandler is nil for connections that don't hold the
	// commands.ReadCapSpaceRead cap — the track is neither created
	// nor announced for those holders, so they don't see the
	// namespace at all. See plan/history/commands/space-read-path-plan.md.
	spaceOutputHandler *SpaceOutputHandler

	// ConnectionHandler - stored so it can be wired to input handlers when they're created
	connectionHandler interface{}

	// Track state
	mu                        sync.RWMutex
	audioInputReady           bool
	stateInputReady           bool
	controlInputReady         bool
	audioOutputAnnounced      bool
	stateOutputAnnounced      bool
	attributesOutputAnnounced bool
	entityOutputAnnounced     bool
	spaceOutputAnnounced      bool

	ctx    context.Context
	cancel context.CancelFunc
}

// NewSubscriptionManager creates a new subscription manager for a session
func NewSubscriptionManager(session *MoqSession, tracks *TrackNames) *SubscriptionManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &SubscriptionManager{
		session:    session,
		moqSession: session.moqSession,
		tracks:     tracks,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start begins listening for client track announcements and sets up subscriptions
func (sm *SubscriptionManager) Start() error {
	common.LogInfo("Starting subscription manager for node %s", sm.tracks.NodeID)

	// Create output handlers FIRST (synchronously) so they're available
	// for SetNodeConfig to wire up, before we start the async announces.

	// Audio output
	audioOutputHandler, err := NewAudioOutputHandler(sm.moqSession, sm.tracks)
	if err != nil {
		return fmt.Errorf("failed to create audio output handler: %w", err)
	}
	if err := audioOutputHandler.Start(); err != nil {
		return fmt.Errorf("failed to start audio output handler: %w", err)
	}

	// State output
	stateOutputHandler, err := NewStateOutputHandler(sm.moqSession, sm.tracks)
	if err != nil {
		return fmt.Errorf("failed to create state output handler: %w", err)
	}
	if err := stateOutputHandler.Start(); err != nil {
		return fmt.Errorf("failed to start state output handler: %w", err)
	}

	// Attributes output
	attributesOutputHandler, err := NewAttributesOutputHandler(sm.moqSession, sm.tracks)
	if err != nil {
		return fmt.Errorf("failed to create attributes output handler: %w", err)
	}
	if err := attributesOutputHandler.Start(); err != nil {
		return fmt.Errorf("failed to start attributes output handler: %w", err)
	}

	// Entity output (per-client filtered)
	entityOutputHandler, err := NewEntityOutputHandler(sm.moqSession, sm.tracks)
	if err != nil {
		return fmt.Errorf("failed to create entity output handler: %w", err)
	}
	if err := entityOutputHandler.Start(); err != nil {
		return fmt.Errorf("failed to start entity output handler: %w", err)
	}

	// Space output (gated by the commands.ReadCapSpaceRead read cap).
	// Only created and announced for holders that have been granted
	// the cap; everyone else never sees the namespace. The cap lookup
	// reads the resolved caps off the session's NodeConfig, populated
	// by the Authoriser at authentication time.
	var spaceOutputHandler *SpaceOutputHandler
	if sm.spaceReadGranted() {
		spaceOutputHandler, err = NewSpaceOutputHandler(sm.moqSession, sm.tracks)
		if err != nil {
			return fmt.Errorf("failed to create space output handler: %w", err)
		}
		if err := spaceOutputHandler.Start(); err != nil {
			return fmt.Errorf("failed to start space output handler: %w", err)
		}
	}

	sm.mu.Lock()
	sm.audioOutputHandler = audioOutputHandler
	sm.stateOutputHandler = stateOutputHandler
	sm.attributesOutputHandler = attributesOutputHandler
	sm.entityOutputHandler = entityOutputHandler
	sm.spaceOutputHandler = spaceOutputHandler
	sm.mu.Unlock()
	common.LogDebug("All output handlers created for node %s", sm.tracks.NodeID)

	// Subscribe to announcements from the client and subscribe to input tracks.
	// These run in a SINGLE goroutine to serialize Subscribe() calls, which
	// prevents track alias races in the moqtransport library. Concurrent
	// Subscribe() calls can cause non-deterministic alias assignment, leading
	// to datagrams being routed to the wrong RemoteTrack handler.
	go sm.handleInputSubscriptions()

	// Announce our output tracks that the client can subscribe to
	// IMPORTANT: These run in goroutines to avoid deadlock.
	// Announce() blocks waiting for ANNOUNCE_OK from the client,
	// but the moqtransport message loop can't read ANNOUNCE_OK until
	// HandleSubscribe returns, which won't happen if we block here.
	go sm.doAnnounceOutputTrack()
	go sm.doAnnounceStateOutputTrack()
	go sm.doAnnounceAttributesOutputTrack()
	go sm.doAnnounceEntityOutputTrack()
	if sm.spaceOutputHandler != nil {
		go sm.doAnnounceSpaceOutputTrack()
	}

	return nil
}

// spaceReadGranted reports whether the holder of this session was
// granted commands.ReadCapSpaceRead. Used to gate creation of the
// space output handler / track announce. Reads off the session's
// NodeConfig which is populated by the Authoriser before
// SetNodeConfig invokes Start().
func (sm *SubscriptionManager) spaceReadGranted() bool {
	if sm.session == nil || sm.session.nodeConfig == nil {
		return false
	}
	return sm.session.nodeConfig.ReadCaps[commands.ReadCapSpaceRead]
}

// handleInputSubscriptions serializes all input track subscriptions to avoid
// track alias races in the moqtransport library.
func (sm *SubscriptionManager) handleInputSubscriptions() {
	sm.handleAnnouncementsForAudio()
	sm.handleAnnouncementsForState()
	sm.handleAnnouncementsForControl()
}

// handleAnnouncementsForAudio listens for and subscribes to audio input announcements
func (sm *SubscriptionManager) handleAnnouncementsForAudio() {
	// Subscribe to announcements with the audio input prefix
	// e.g., "/in/audio/opus-mono/{nodeId}"
	prefix := sm.tracks.AudioInputNamespace[:len(sm.tracks.AudioInputNamespace)-1] // ["in", "audio", "opus-mono"]

	common.LogDebug("Subscribing to announcements with prefix: %v", prefix)

	err := sm.moqSession.SubscribeAnnouncements(sm.ctx, prefix)
	if err != nil {
		common.LogError("Failed to subscribe to audio announcements: %v", err)
		return
	}

	// When the client announces this track, subscribe to it
	// This will be handled via the Handler interface
	// For now, we'll directly subscribe since we know the track name
	sm.subscribeToAudioInput()
}

// handleAnnouncementsForState listens for and subscribes to state input announcements
func (sm *SubscriptionManager) handleAnnouncementsForState() {
	// Subscribe to announcements with the state input prefix
	// e.g., "/state/{nodeId}"
	prefix := sm.tracks.StateInputNamespace[:len(sm.tracks.StateInputNamespace)-1] // ["state"]

	common.LogDebug("Subscribing to announcements with prefix: %v", prefix)

	err := sm.moqSession.SubscribeAnnouncements(sm.ctx, prefix)
	if err != nil {
		common.LogError("Failed to subscribe to state announcements: %v", err)
		return
	}

	// Subscribe to the state track
	sm.subscribeToStateInput()
}

// subscribeToAudioInput subscribes to the client's audio input track
func (sm *SubscriptionManager) subscribeToAudioInput() {
	common.LogInfo("Subscribing to audio input track: %v", sm.tracks.AudioInputNamespace)

	track, err := sm.moqSession.Subscribe(
		sm.ctx,
		sm.tracks.AudioInputNamespace,
		"", // Track name (empty for namespace-only subscription)
	)

	if err != nil {
		common.LogError("Failed to subscribe to audio input track for node %s: %v", sm.tracks.NodeID, err)
		// Don't return - this is not fatal, client may not be publishing audio yet
		return
	}

	sm.mu.Lock()
	sm.audioInputTrack = track
	sm.audioInputReady = true
	sm.mu.Unlock()

	common.LogInfo("Successfully subscribed to audio input track for node %s", sm.tracks.NodeID)

	// Create and start audio input handler
	handler := NewAudioInputHandler(track, sm.tracks)

	// Wire ConnectionHandler if already set (fixes race condition)
	sm.mu.RLock()
	connHandler := sm.connectionHandler
	sm.mu.RUnlock()
	if connHandler != nil {
		handler.SetConnectionHandler(connHandler)
		common.LogDebug("Audio input handler wired to ConnectionHandler on creation")
	}

	if err := handler.Start(); err != nil {
		common.LogError("Failed to start audio input handler for node %s: %v", sm.tracks.NodeID, err)
		// Clean up the track subscription
		sm.mu.Lock()
		sm.audioInputReady = false
		sm.audioInputTrack = nil
		sm.mu.Unlock()
		return
	}

	sm.mu.Lock()
	sm.audioInputHandler = handler
	sm.mu.Unlock()

	common.LogDebug("Audio input handler started for node %s", sm.tracks.NodeID)
}

// subscribeToStateInput subscribes to the client's state input track
func (sm *SubscriptionManager) subscribeToStateInput() {
	common.LogInfo("Subscribing to state input track: %v", sm.tracks.StateInputNamespace)

	track, err := sm.moqSession.Subscribe(
		sm.ctx,
		sm.tracks.StateInputNamespace,
		"", // Track name
	)

	if err != nil {
		common.LogError("Failed to subscribe to state input track for node %s: %v", sm.tracks.NodeID, err)
		// Don't return - this is not fatal, client may not be publishing state yet
		return
	}

	sm.mu.Lock()
	sm.stateInputTrack = track
	sm.stateInputReady = true
	sm.mu.Unlock()

	common.LogInfo("Successfully subscribed to state input track for node %s", sm.tracks.NodeID)

	// Create and start state input handler
	handler := NewStateInputHandler(track, sm.tracks)

	// Wire ConnectionHandler if already set (fixes race condition)
	sm.mu.RLock()
	connHandler := sm.connectionHandler
	sm.mu.RUnlock()
	if connHandler != nil {
		handler.SetConnectionHandler(connHandler)
		common.LogDebug("State input handler wired to ConnectionHandler on creation")
	}

	if err := handler.Start(); err != nil {
		common.LogError("Failed to start state input handler for node %s: %v", sm.tracks.NodeID, err)
		// Clean up the track subscription
		sm.mu.Lock()
		sm.stateInputReady = false
		sm.stateInputTrack = nil
		sm.mu.Unlock()
		return
	}

	sm.mu.Lock()
	sm.stateInputHandler = handler
	sm.mu.Unlock()

	common.LogDebug("State input handler started for node %s", sm.tracks.NodeID)
}

// handleAnnouncementsForControl listens for and subscribes to control input announcements
func (sm *SubscriptionManager) handleAnnouncementsForControl() {
	// Subscribe to announcements with the control input prefix
	// e.g., "/in/control/{nodeId}"
	prefix := sm.tracks.ControlInputNamespace[:len(sm.tracks.ControlInputNamespace)-1] // ["in", "control"]

	common.LogDebug("Subscribing to announcements with prefix: %v", prefix)

	err := sm.moqSession.SubscribeAnnouncements(sm.ctx, prefix)
	if err != nil {
		common.LogError("Failed to subscribe to control announcements: %v", err)
		return
	}

	sm.subscribeToControlInput()
}

// subscribeToControlInput subscribes to the client's control input track
func (sm *SubscriptionManager) subscribeToControlInput() {
	common.LogInfo("Subscribing to control input track: %v", sm.tracks.ControlInputNamespace)

	track, err := sm.moqSession.Subscribe(
		sm.ctx,
		sm.tracks.ControlInputNamespace,
		"",
	)

	if err != nil {
		common.LogError("Failed to subscribe to control input track for node %s: %v", sm.tracks.NodeID, err)
		return
	}

	sm.mu.Lock()
	sm.controlInputTrack = track
	sm.controlInputReady = true
	sm.mu.Unlock()

	common.LogInfo("Successfully subscribed to control input track for node %s", sm.tracks.NodeID)

	handler := NewControlInputHandler(track, sm.tracks)

	sm.mu.RLock()
	connHandler := sm.connectionHandler
	sm.mu.RUnlock()
	if connHandler != nil {
		handler.SetConnectionHandler(connHandler)
		common.LogDebug("Control input handler wired to ConnectionHandler on creation")
	}

	if err := handler.Start(); err != nil {
		common.LogError("Failed to start control input handler for node %s: %v", sm.tracks.NodeID, err)
		sm.mu.Lock()
		sm.controlInputReady = false
		sm.controlInputTrack = nil
		sm.mu.Unlock()
		return
	}

	sm.mu.Lock()
	sm.controlInputHandler = handler
	sm.mu.Unlock()

	common.LogDebug("Control input handler started for node %s", sm.tracks.NodeID)
}

// doAnnounceOutputTrack sends the ANNOUNCE message for the output track.
// The handler should already be created before calling this.
func (sm *SubscriptionManager) doAnnounceOutputTrack() {
	common.LogInfo("Announcing audio output track: %v", sm.tracks.AudioOutputNamespace)

	err := sm.moqSession.Announce(sm.ctx, sm.tracks.AudioOutputNamespace)
	if err != nil {
		common.LogError("Failed to announce audio output track: %v", err)
		return
	}

	sm.mu.Lock()
	sm.audioOutputAnnounced = true
	sm.mu.Unlock()

	common.LogInfo("Successfully announced audio output track")
}

// doAnnounceStateOutputTrack sends the ANNOUNCE message for the state output track
func (sm *SubscriptionManager) doAnnounceStateOutputTrack() {
	common.LogInfo("Announcing state output track: %v", sm.tracks.StateOutputNamespace)

	err := sm.moqSession.Announce(sm.ctx, sm.tracks.StateOutputNamespace)
	if err != nil {
		common.LogError("Failed to announce state output track: %v", err)
		return
	}

	sm.mu.Lock()
	sm.stateOutputAnnounced = true
	sm.mu.Unlock()

	common.LogInfo("Successfully announced state output track")
}

// doAnnounceAttributesOutputTrack sends the ANNOUNCE message for the attributes output track
func (sm *SubscriptionManager) doAnnounceAttributesOutputTrack() {
	common.LogInfo("Announcing attributes output track: %v", sm.tracks.AttributesOutputNamespace)

	err := sm.moqSession.Announce(sm.ctx, sm.tracks.AttributesOutputNamespace)
	if err != nil {
		common.LogError("Failed to announce attributes output track: %v", err)
		return
	}

	sm.mu.Lock()
	sm.attributesOutputAnnounced = true
	sm.mu.Unlock()

	common.LogInfo("Successfully announced attributes output track")
}

// doAnnounceEntityOutputTrack sends the ANNOUNCE message for the entity output track
func (sm *SubscriptionManager) doAnnounceEntityOutputTrack() {
	common.LogInfo("Announcing entity output track: %v", sm.tracks.EntityOutputNamespace)

	err := sm.moqSession.Announce(sm.ctx, sm.tracks.EntityOutputNamespace)
	if err != nil {
		common.LogError("Failed to announce entity output track: %v", err)
		return
	}

	sm.mu.Lock()
	sm.entityOutputAnnounced = true
	sm.mu.Unlock()

	common.LogInfo("Successfully announced entity output track")
}

// doAnnounceSpaceOutputTrack sends the ANNOUNCE message for the space
// output track. Only invoked when spaceOutputHandler is non-nil (i.e.
// the holder has the commands.ReadCapSpaceRead cap), so unauthorised
// holders never see the namespace.
func (sm *SubscriptionManager) doAnnounceSpaceOutputTrack() {
	common.LogInfo("Announcing space output track: %v", sm.tracks.SpaceOutputNamespace)

	err := sm.moqSession.Announce(sm.ctx, sm.tracks.SpaceOutputNamespace)
	if err != nil {
		common.LogError("Failed to announce space output track: %v", err)
		return
	}

	sm.mu.Lock()
	sm.spaceOutputAnnounced = true
	sm.mu.Unlock()

	common.LogInfo("Successfully announced space output track")
}

// GetAudioInputTrack returns the subscribed audio input track (if ready)
func (sm *SubscriptionManager) GetAudioInputTrack() (*moqtransport.RemoteTrack, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.audioInputTrack, sm.audioInputReady
}

// GetStateInputTrack returns the subscribed state input track (if ready)
func (sm *SubscriptionManager) GetStateInputTrack() (*moqtransport.RemoteTrack, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.stateInputTrack, sm.stateInputReady
}

// IsOutputAnnounced returns whether the output track has been announced
func (sm *SubscriptionManager) IsOutputAnnounced() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.audioOutputAnnounced
}

// GetAudioOutputAdapter returns the track adapter for ConnectionHandler integration
func (sm *SubscriptionManager) GetAudioOutputAdapter() *MoqTrackAdapter {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.audioOutputHandler == nil {
		return nil
	}
	return sm.audioOutputHandler.GetTrackAdapter()
}

// GetStateOutputAdapter returns the state output track adapter
func (sm *SubscriptionManager) GetStateOutputAdapter() *MoqTrackAdapter {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.stateOutputHandler == nil {
		return nil
	}
	return sm.stateOutputHandler.GetTrackAdapter()
}

// GetAttributesOutputAdapter returns the attributes output track adapter
func (sm *SubscriptionManager) GetAttributesOutputAdapter() *MoqTrackAdapter {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.attributesOutputHandler == nil {
		return nil
	}
	return sm.attributesOutputHandler.GetTrackAdapter()
}

// GetEntityOutputAdapter returns the entity output track adapter
func (sm *SubscriptionManager) GetEntityOutputAdapter() *MoqTrackAdapter {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.entityOutputHandler == nil {
		return nil
	}
	return sm.entityOutputHandler.GetTrackAdapter()
}

// GetSpaceOutputAdapter returns the space output track adapter, or
// nil for connections without the commands.ReadCapSpaceRead cap (the
// handler is never created in that case).
func (sm *SubscriptionManager) GetSpaceOutputAdapter() *MoqTrackAdapter {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.spaceOutputHandler == nil {
		return nil
	}
	return sm.spaceOutputHandler.GetTrackAdapter()
}

// SetConnectionHandler wires the input handlers to the ConnectionHandler
func (sm *SubscriptionManager) SetConnectionHandler(handler interface{}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Store the handler so we can wire it to handlers created later
	sm.connectionHandler = handler
	common.LogDebug("ConnectionHandler stored in SubscriptionManager")

	// Wire audio input handler if it already exists
	if sm.audioInputHandler != nil {
		sm.audioInputHandler.SetConnectionHandler(handler)
		common.LogDebug("Audio input handler wired to ConnectionHandler")
	}

	// Wire state input handler if it already exists
	if sm.stateInputHandler != nil {
		sm.stateInputHandler.SetConnectionHandler(handler)
		common.LogDebug("State input handler wired to ConnectionHandler")
	}

	// Wire control input handler if it already exists
	if sm.controlInputHandler != nil {
		sm.controlInputHandler.SetConnectionHandler(handler)
		common.LogDebug("Control input handler wired to ConnectionHandler")
	}
}

// Close stops the subscription manager and cleans up resources
func (sm *SubscriptionManager) Close() error {
	common.LogDebug("Closing subscription manager for node %s", sm.tracks.NodeID)

	// Unannounce our output tracks BEFORE cancelling sm.ctx: these are
	// control messages to the client and previously went out on the
	// already-cancelled context, so on a clean disconnect they likely
	// never left the server. A short independent deadline bounds Close
	// when the transport is already dead (abrupt disconnect).
	unannounceCtx, unannounceCancel := context.WithTimeout(context.Background(), time.Second)
	defer unannounceCancel()
	if sm.audioOutputAnnounced {
		if err := sm.moqSession.Unannounce(unannounceCtx, sm.tracks.AudioOutputNamespace); err != nil {
			logCloseError("Failed to unannounce audio output track: %v", err)
		}
	}
	if sm.stateOutputAnnounced {
		if err := sm.moqSession.Unannounce(unannounceCtx, sm.tracks.StateOutputNamespace); err != nil {
			logCloseError("Failed to unannounce state output track: %v", err)
		}
	}
	if sm.attributesOutputAnnounced {
		if err := sm.moqSession.Unannounce(unannounceCtx, sm.tracks.AttributesOutputNamespace); err != nil {
			logCloseError("Failed to unannounce attributes output track: %v", err)
		}
	}
	if sm.entityOutputAnnounced {
		if err := sm.moqSession.Unannounce(unannounceCtx, sm.tracks.EntityOutputNamespace); err != nil {
			logCloseError("Failed to unannounce entity output track: %v", err)
		}
	}
	if sm.spaceOutputAnnounced {
		if err := sm.moqSession.Unannounce(unannounceCtx, sm.tracks.SpaceOutputNamespace); err != nil {
			logCloseError("Failed to unannounce space output track: %v", err)
		}
	}

	sm.cancel()

	// Stop audio input handler
	if sm.audioInputHandler != nil {
		if err := sm.audioInputHandler.Stop(); err != nil {
			logCloseError("Failed to stop audio input handler: %v", err)
		}
	}

	// Stop state input handler
	if sm.stateInputHandler != nil {
		if err := sm.stateInputHandler.Stop(); err != nil {
			logCloseError("Failed to stop state input handler: %v", err)
		}
	}

	// Stop control input handler
	if sm.controlInputHandler != nil {
		if err := sm.controlInputHandler.Stop(); err != nil {
			logCloseError("Failed to stop control input handler: %v", err)
		}
	}

	// Stop audio output handler
	if sm.audioOutputHandler != nil {
		if err := sm.audioOutputHandler.Stop(); err != nil {
			logCloseError("Failed to stop audio output handler: %v", err)
		}
	}

	// Stop state output handler
	if sm.stateOutputHandler != nil {
		if err := sm.stateOutputHandler.Stop(); err != nil {
			logCloseError("Failed to stop state output handler: %v", err)
		}
	}

	// Stop attributes output handler
	if sm.attributesOutputHandler != nil {
		if err := sm.attributesOutputHandler.Stop(); err != nil {
			logCloseError("Failed to stop attributes output handler: %v", err)
		}
	}

	// Stop entity output handler
	if sm.entityOutputHandler != nil {
		if err := sm.entityOutputHandler.Stop(); err != nil {
			logCloseError("Failed to stop entity output handler: %v", err)
		}
	}

	// Stop space output handler (only created when the holder has
	// commands.ReadCapSpaceRead).
	if sm.spaceOutputHandler != nil {
		if err := sm.spaceOutputHandler.Stop(); err != nil {
			logCloseError("Failed to stop space output handler: %v", err)
		}
	}

	// Note: Remote tracks are closed by each input handler's Stop.

	return nil
}
