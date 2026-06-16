package moq

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/panaudia/panaudia/core/common"
	"github.com/pion/webrtc/v3/pkg/media"
)

// pendingMaxBytes caps the pre-publisher buffer to bound memory if a
// publisher never subscribes. 1 MiB is large enough for any realistic
// per-client entity / attributes backfill (typical: <1 KiB) and small
// enough not to leak meaningfully on a stuck session.
const pendingMaxBytes = 1 * 1024 * 1024

// MoqTrackAdapter adapts MOQ publishing to the webrtc.TrackLocalStaticSample interface
// This allows ConnectionHandler to write samples to a MOQ track instead of WebRTC
type MoqTrackAdapter struct {
	moqSession *moqtransport.Session
	namespace  []string
	trackName  string

	// Publishers for subscribed clients
	mu         sync.RWMutex
	publishers []moqtransport.Publisher

	// Pre-publisher buffer: writes that arrive before the client has
	// completed SUBSCRIBE land here, then drain to the first publisher
	// when AddPublisher is called. Without this the backfill goroutine
	// races the client's SUBSCRIBE — any envelope WriteSample receives
	// before the first publisher is silently lost. That bites the
	// entity track in particular because entity ops written *about* a
	// node (e.g. an admin's `{node}.muted`) only ever reach the client
	// via backfill: the node's own sendEntity loop never re-emits them.
	pending      [][]byte
	pendingBytes int

	// Object sequencing
	groupID    atomic.Uint64 // timestamp-based group ID
	objectID   atomic.Uint64 // sequential object ID within a group
	lastUpdate time.Time

	// Statistics
	objectsPublished atomic.Uint64
	bytesPublished   atomic.Uint64
	publishErrors    atomic.Uint64

	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

// NewMoqTrackAdapter creates a new MOQ track adapter for audio output
func NewMoqTrackAdapter(moqSession *moqtransport.Session, namespace []string) (*MoqTrackAdapter, error) {
	ctx, cancel := context.WithCancel(context.Background())

	adapter := &MoqTrackAdapter{
		moqSession: moqSession,
		namespace:  namespace,
		trackName:  "", // Empty track name for namespace-only tracks
		ctx:        ctx,
		cancel:     cancel,
		lastUpdate: time.Now(),
		publishers: make([]moqtransport.Publisher, 0),
	}

	// Initialize group ID with current timestamp in milliseconds
	adapter.groupID.Store(uint64(time.Now().UnixMilli()))

	return adapter, nil
}

// Start announces the track and begins accepting samples
func (a *MoqTrackAdapter) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return fmt.Errorf("track adapter already started")
	}

	// The track should already be announced by SubscriptionManager
	// We're ready to accept publishers when clients subscribe
	a.started = true

	common.LogInfo("MOQ track adapter started for namespace: %v", a.namespace)
	return nil
}

// AddPublisher adds a publisher when a client subscribes to our output track.
// If samples arrived before any publisher was attached (most commonly: the
// session-start backfill races the client's SUBSCRIBE), they were buffered
// in `pending` and are flushed here so the catch-up state actually lands.
func (a *MoqTrackAdapter) AddPublisher(publisher moqtransport.Publisher) {
	a.mu.Lock()
	a.publishers = append(a.publishers, publisher)
	pending := a.pending
	a.pending = nil
	a.pendingBytes = 0
	publisherIndex := len(a.publishers) - 1
	a.mu.Unlock()

	common.LogDebug("Added publisher for output track (total: %d, draining %d pending samples)",
		publisherIndex+1, len(pending))

	// Drain to the just-added publisher only. Other publishers (none in
	// the per-client model used today) wouldn't have been the intended
	// recipients of these buffered samples anyway.
	if len(pending) == 0 {
		return
	}
	for _, data := range pending {
		groupID := a.groupID.Load()
		objectID := a.objectID.Add(1) - 1
		obj := moqtransport.Object{
			GroupID:  groupID,
			ObjectID: objectID,
			Payload:  data,
		}
		if err := publisher.SendDatagram(obj); err != nil {
			a.publishErrors.Add(1)
			common.LogError("Failed to drain pending sample to new publisher (namespace: %v): %v", a.namespace, err)
			continue
		}
		a.objectsPublished.Add(1)
		a.bytesPublished.Add(uint64(len(data)))
	}
}

// RemovePublisher removes a publisher when a client unsubscribes
func (a *MoqTrackAdapter) RemovePublisher(publisher moqtransport.Publisher) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, p := range a.publishers {
		if p == publisher {
			a.publishers = append(a.publishers[:i], a.publishers[i+1:]...)
			common.LogDebug("Removed publisher from output track (total: %d)", len(a.publishers))
			return
		}
	}
}

// WriteSample publishes an Opus sample as a MOQ object to all subscribed clients
// This is the main interface used by ConnectionHandler.WriteAmbisonic()
func (a *MoqTrackAdapter) WriteSample(sample media.Sample) error {
	// Fast path: most calls hit a track that already has publishers.
	// Take RLock first; only escalate to a Lock when the no-publisher
	// branch needs to mutate the pending buffer.
	a.mu.RLock()
	if !a.started {
		a.mu.RUnlock()
		return fmt.Errorf("track adapter not started")
	}

	if len(a.publishers) == 0 {
		a.mu.RUnlock()
		// No publishers yet — buffer for the first one to arrive. Take
		// a defensive copy of the payload because callers may reuse
		// the underlying slice after WriteSample returns.
		a.mu.Lock()
		// Re-check started under the write lock in case Stop ran
		// between the RUnlock above and here.
		if !a.started {
			a.mu.Unlock()
			return fmt.Errorf("track adapter not started")
		}
		// Late publisher could have arrived between the RUnlock and
		// the Lock; if so, fall through to the publish path.
		if len(a.publishers) == 0 {
			cp := make([]byte, len(sample.Data))
			copy(cp, sample.Data)
			// Drop oldest samples first if the buffer would exceed the cap.
			for a.pendingBytes+len(cp) > pendingMaxBytes && len(a.pending) > 0 {
				a.pendingBytes -= len(a.pending[0])
				a.pending = a.pending[1:]
			}
			a.pending = append(a.pending, cp)
			a.pendingBytes += len(cp)
			a.mu.Unlock()
			return nil
		}
		// Publisher appeared concurrently — downgrade to RLock and continue.
		a.mu.Unlock()
		a.mu.RLock()
	}

	// Update group ID based on time (new group every ~1 second or so)
	now := time.Now()
	if now.Sub(a.lastUpdate) > time.Second {
		a.groupID.Store(uint64(now.UnixMilli()))
		a.objectID.Store(0) // Reset object ID for new group
		a.lastUpdate = now
	}

	groupID := a.groupID.Load()
	objectID := a.objectID.Add(1) - 1 // Get current value then increment

	// Create MOQ object with Opus data as payload
	obj := moqtransport.Object{
		GroupID:  groupID,
		ObjectID: objectID,
		Payload:  sample.Data,
	}

	// Publish to all subscribed clients while holding RLock.
	// SendDatagram is a fast datagram write; publisher changes only
	// happen at connect/disconnect so contention is negligible.
	var lastErr error
	successCount := 0
	for i, publisher := range a.publishers {
		if err := publisher.SendDatagram(obj); err != nil {
			a.publishErrors.Add(1)
			lastErr = err
			if a.publishErrors.Load() < 10 {
				common.LogError("Failed to send datagram to publisher %d (namespace: %v): %v", i, a.namespace, err)
			} else if a.publishErrors.Load()%100 == 0 {
				common.LogWarn("Frequent publish errors to namespace %v (count: %d)", a.namespace, a.publishErrors.Load())
			}
		} else {
			successCount++
		}
	}
	a.mu.RUnlock()

	// Update statistics if at least one publish succeeded
	if successCount > 0 {
		a.objectsPublished.Add(1)
		a.bytesPublished.Add(uint64(len(sample.Data)))
	}

	// Return error only if all publishes failed
	if successCount == 0 && lastErr != nil {
		return fmt.Errorf("failed to publish to any client: %w", lastErr)
	}

	return nil
}

// Stop closes the track adapter and cleans up resources
func (a *MoqTrackAdapter) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.started {
		return nil
	}

	a.cancel()

	// Close all publishers
	for _, publisher := range a.publishers {
		if err := publisher.CloseWithError(0, "track adapter stopped"); err != nil {
			logCloseError("Error closing publisher: %v", err)
		}
	}
	a.publishers = nil
	a.pending = nil
	a.pendingBytes = 0

	a.started = false

	common.LogDebug("MOQ track adapter stopped. Published %d objects (%d bytes), %d errors",
		a.objectsPublished.Load(), a.bytesPublished.Load(), a.publishErrors.Load())

	return nil
}

// GetStats returns current publishing statistics
func (a *MoqTrackAdapter) GetStats() (objectsPublished, bytesPublished, publishErrors uint64) {
	return a.objectsPublished.Load(), a.bytesPublished.Load(), a.publishErrors.Load()
}
