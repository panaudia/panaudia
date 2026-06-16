package moq

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/pion/webrtc/v3/pkg/media"
)

// errMockClosed is used for testing error scenarios
var errMockClosed = errors.New("mock publisher closed")

// mockPublisher implements moqtransport.Publisher for testing
type mockPublisher struct {
	mu            sync.Mutex
	sentDatagrams []moqtransport.Object
	shouldError   bool
	closed        bool
	closeErr      error
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{
		sentDatagrams: make([]moqtransport.Object, 0),
	}
}

func (p *mockPublisher) SendDatagram(obj moqtransport.Object) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shouldError {
		return errMockClosed
	}
	p.sentDatagrams = append(p.sentDatagrams, obj)
	return nil
}

func (p *mockPublisher) OpenSubgroup(groupID, subgroupID uint64, priority uint8, opts ...moqtransport.SubgroupOption) (*moqtransport.Subgroup, error) {
	// Not used in our adapter, return nil
	return nil, nil
}

func (p *mockPublisher) CloseWithError(code uint64, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return p.closeErr
}

func (p *mockPublisher) getSentDatagrams() []moqtransport.Object {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]moqtransport.Object, len(p.sentDatagrams))
	copy(result, p.sentDatagrams)
	return result
}

// TestMoqTrackAdapterWriteSampleNoPublishers tests that WriteSample succeeds with no publishers
func TestMoqTrackAdapterWriteSampleNoPublishers(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
	}

	sample := media.Sample{
		Data: []byte{0x01, 0x02, 0x03, 0x04}, // Fake Opus data
	}

	// Should succeed with no error when no publishers
	err := adapter.WriteSample(sample)
	if err != nil {
		t.Errorf("Expected no error with no publishers, got: %v", err)
	}
}

// TestMoqTrackAdapterWriteSampleSinglePublisher tests publishing to a single publisher
func TestMoqTrackAdapterWriteSampleSinglePublisher(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
		lastUpdate: time.Now(),
	}
	adapter.groupID.Store(1000)
	adapter.objectID.Store(0)

	publisher := newMockPublisher()
	adapter.AddPublisher(publisher)

	sample := media.Sample{
		Data: []byte{0x01, 0x02, 0x03, 0x04},
	}

	err := adapter.WriteSample(sample)
	if err != nil {
		t.Fatalf("WriteSample failed: %v", err)
	}

	datagrams := publisher.getSentDatagrams()
	if len(datagrams) != 1 {
		t.Fatalf("Expected 1 datagram, got %d", len(datagrams))
	}

	if len(datagrams[0].Payload) != 4 {
		t.Errorf("Expected payload length 4, got %d", len(datagrams[0].Payload))
	}

	// Verify stats
	objPublished, bytesPublished, _ := adapter.GetStats()
	if objPublished != 1 {
		t.Errorf("Expected 1 object published, got %d", objPublished)
	}
	if bytesPublished != 4 {
		t.Errorf("Expected 4 bytes published, got %d", bytesPublished)
	}
}

// TestMoqTrackAdapterWriteSampleMultiplePublishers tests publishing to multiple publishers
func TestMoqTrackAdapterWriteSampleMultiplePublishers(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
		lastUpdate: time.Now(),
	}
	adapter.groupID.Store(1000)
	adapter.objectID.Store(0)

	publisher1 := newMockPublisher()
	publisher2 := newMockPublisher()
	publisher3 := newMockPublisher()

	adapter.AddPublisher(publisher1)
	adapter.AddPublisher(publisher2)
	adapter.AddPublisher(publisher3)

	sample := media.Sample{
		Data: []byte{0x01, 0x02, 0x03},
	}

	err := adapter.WriteSample(sample)
	if err != nil {
		t.Fatalf("WriteSample failed: %v", err)
	}

	// All three publishers should receive the datagram
	for i, pub := range []*mockPublisher{publisher1, publisher2, publisher3} {
		datagrams := pub.getSentDatagrams()
		if len(datagrams) != 1 {
			t.Errorf("Publisher %d: expected 1 datagram, got %d", i+1, len(datagrams))
		}
	}
}

// TestMoqTrackAdapterWriteSamplePartialFailure tests that partial failures don't fail the overall operation
func TestMoqTrackAdapterWriteSamplePartialFailure(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
		lastUpdate: time.Now(),
	}
	adapter.groupID.Store(1000)
	adapter.objectID.Store(0)

	publisher1 := newMockPublisher()
	publisher2 := newMockPublisher()
	publisher2.shouldError = true // This one will fail
	publisher3 := newMockPublisher()

	adapter.AddPublisher(publisher1)
	adapter.AddPublisher(publisher2)
	adapter.AddPublisher(publisher3)

	sample := media.Sample{
		Data: []byte{0x01, 0x02, 0x03},
	}

	err := adapter.WriteSample(sample)
	// Should succeed because some publishers succeeded
	if err != nil {
		t.Errorf("Expected success with partial failures, got: %v", err)
	}

	// Verify the working publishers received the datagram
	if len(publisher1.getSentDatagrams()) != 1 {
		t.Errorf("Publisher 1 should have received datagram")
	}
	if len(publisher2.getSentDatagrams()) != 0 {
		t.Errorf("Publisher 2 should not have received datagram (error)")
	}
	if len(publisher3.getSentDatagrams()) != 1 {
		t.Errorf("Publisher 3 should have received datagram")
	}

	// Verify error was counted
	_, _, publishErrors := adapter.GetStats()
	if publishErrors != 1 {
		t.Errorf("Expected 1 publish error, got %d", publishErrors)
	}
}

// TestMoqTrackAdapterWriteSampleAllFailure tests that all publishers failing returns an error
func TestMoqTrackAdapterWriteSampleAllFailure(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
		lastUpdate: time.Now(),
	}
	adapter.groupID.Store(1000)
	adapter.objectID.Store(0)

	publisher1 := newMockPublisher()
	publisher1.shouldError = true
	publisher2 := newMockPublisher()
	publisher2.shouldError = true

	adapter.AddPublisher(publisher1)
	adapter.AddPublisher(publisher2)

	sample := media.Sample{
		Data: []byte{0x01, 0x02, 0x03},
	}

	err := adapter.WriteSample(sample)
	// Should fail because all publishers failed
	if err == nil {
		t.Error("Expected error when all publishers fail")
	}
}

// TestMoqTrackAdapterNotStarted tests WriteSample when adapter is not started
func TestMoqTrackAdapterNotStarted(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    false, // Not started
	}

	sample := media.Sample{
		Data: []byte{0x01, 0x02, 0x03},
	}

	err := adapter.WriteSample(sample)
	if err == nil {
		t.Error("Expected error when adapter not started")
	}
}

// TestMoqTrackAdapterAddRemovePublisher tests adding and removing publishers
func TestMoqTrackAdapterAddRemovePublisher(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
	}

	publisher1 := newMockPublisher()
	publisher2 := newMockPublisher()

	// Add publishers
	adapter.AddPublisher(publisher1)
	adapter.AddPublisher(publisher2)

	adapter.mu.RLock()
	if len(adapter.publishers) != 2 {
		t.Errorf("Expected 2 publishers, got %d", len(adapter.publishers))
	}
	adapter.mu.RUnlock()

	// Remove one
	adapter.RemovePublisher(publisher1)

	adapter.mu.RLock()
	if len(adapter.publishers) != 1 {
		t.Errorf("Expected 1 publisher after removal, got %d", len(adapter.publishers))
	}
	adapter.mu.RUnlock()

	// Remove non-existent (should not panic)
	adapter.RemovePublisher(publisher1)

	adapter.mu.RLock()
	if len(adapter.publishers) != 1 {
		t.Errorf("Expected 1 publisher after removing non-existent, got %d", len(adapter.publishers))
	}
	adapter.mu.RUnlock()
}

// TestMoqTrackAdapterObjectSequencing tests that object IDs increment correctly
func TestMoqTrackAdapterObjectSequencing(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
		lastUpdate: time.Now(),
	}
	adapter.groupID.Store(1000)
	adapter.objectID.Store(0)

	publisher := newMockPublisher()
	adapter.AddPublisher(publisher)

	// Send multiple samples
	for i := 0; i < 5; i++ {
		sample := media.Sample{
			Data: []byte{byte(i)},
		}
		if err := adapter.WriteSample(sample); err != nil {
			t.Fatalf("WriteSample %d failed: %v", i, err)
		}
	}

	datagrams := publisher.getSentDatagrams()
	if len(datagrams) != 5 {
		t.Fatalf("Expected 5 datagrams, got %d", len(datagrams))
	}

	// Verify object IDs increment
	for i, dg := range datagrams {
		if dg.ObjectID != uint64(i) {
			t.Errorf("Expected ObjectID %d, got %d", i, dg.ObjectID)
		}
		if dg.GroupID != 1000 {
			t.Errorf("Expected GroupID 1000, got %d", dg.GroupID)
		}
	}
}

// TestMoqTrackAdapterStop tests stopping the adapter
func TestMoqTrackAdapterStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
		ctx:        ctx,
		cancel:     cancel,
	}

	publisher1 := newMockPublisher()
	publisher2 := newMockPublisher()
	adapter.AddPublisher(publisher1)
	adapter.AddPublisher(publisher2)

	err := adapter.Stop()
	if err != nil {
		t.Errorf("Stop failed: %v", err)
	}

	// Verify adapter is stopped
	if adapter.started {
		t.Error("Adapter should be stopped")
	}

	// Verify publishers were closed
	if !publisher1.closed {
		t.Error("Publisher 1 should be closed")
	}
	if !publisher2.closed {
		t.Error("Publisher 2 should be closed")
	}

	// Verify publishers list is cleared
	if len(adapter.publishers) != 0 {
		t.Errorf("Publishers should be cleared, got %d", len(adapter.publishers))
	}
}

// TestMoqTrackAdapterConcurrentWrites tests concurrent access
func TestMoqTrackAdapterConcurrentWrites(t *testing.T) {
	adapter := &MoqTrackAdapter{
		namespace:  []string{"out", "audio", "opus-stereo", "test-node"},
		publishers: make([]moqtransport.Publisher, 0),
		started:    true,
		lastUpdate: time.Now(),
	}
	adapter.groupID.Store(1000)
	adapter.objectID.Store(0)

	publisher := newMockPublisher()
	adapter.AddPublisher(publisher)

	var wg sync.WaitGroup
	numGoroutines := 10
	samplesPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < samplesPerGoroutine; j++ {
				sample := media.Sample{
					Data: []byte{byte(id), byte(j)},
				}
				_ = adapter.WriteSample(sample)
			}
		}(i)
	}

	wg.Wait()

	// Verify all samples were published
	objPublished, _, _ := adapter.GetStats()
	expected := uint64(numGoroutines * samplesPerGoroutine)
	if objPublished != expected {
		t.Errorf("Expected %d objects published, got %d", expected, objPublished)
	}
}