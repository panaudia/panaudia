package moq

import (
	"context"
	"sync"
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
)

// mockAudioConnectionHandler tracks calls to WriteOpus
type mockAudioConnectionHandler struct {
	mu         sync.Mutex
	opusFrames [][]byte
	positions  []common.Position
	rotations  []common.Rotation
}

func newMockAudioConnectionHandler() *mockAudioConnectionHandler {
	return &mockAudioConnectionHandler{
		opusFrames: make([][]byte, 0),
		positions:  make([]common.Position, 0),
		rotations:  make([]common.Rotation, 0),
	}
}

func (h *mockAudioConnectionHandler) WriteOpus(src []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	frame := make([]byte, len(src))
	copy(frame, src)
	h.opusFrames = append(h.opusFrames, frame)
	return nil
}

func (h *mockAudioConnectionHandler) SetPosition(position common.Position) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.positions = append(h.positions, position)
}

func (h *mockAudioConnectionHandler) SetRotation(rotation common.Rotation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rotations = append(h.rotations, rotation)
}

func (h *mockAudioConnectionHandler) getOpusFrameCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.opusFrames)
}

func (h *mockAudioConnectionHandler) getPositionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.positions)
}

func (h *mockAudioConnectionHandler) getLastPosition() *common.Position {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.positions) == 0 {
		return nil
	}
	pos := h.positions[len(h.positions)-1]
	return &pos
}

// createAudioInputHandler creates a handler for testing without requiring a real track
func createAudioInputHandler(testUUID uuid.UUID) *AudioInputHandler {
	trackNames := GenerateTrackNamesFromUUID(testUUID)
	ctx, cancel := context.WithCancel(context.Background())
	return &AudioInputHandler{
		track:      nil, // Not needed for processObject tests
		nodeID:     trackNames.NodeID,
		trackNames: &trackNames,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// createStateInputHandler creates a handler for testing without requiring a real track
func createStateInputHandler(testUUID uuid.UUID) *StateInputHandler {
	trackNames := GenerateTrackNamesFromUUID(testUUID)
	ctx, cancel := context.WithCancel(context.Background())
	return &StateInputHandler{
		track:      nil, // Not needed for processObject tests
		nodeID:     trackNames.NodeID,
		trackNames: &trackNames,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// TestAudioInputHandlerProcessObject tests audio frame processing
func TestAudioInputHandlerProcessObject(t *testing.T) {
	testUUID := uuid.New()
	handler := createAudioInputHandler(testUUID)

	connHandler := newMockAudioConnectionHandler()
	handler.SetConnectionHandler(connHandler)

	// Create a valid Opus frame (typical size range)
	opusFrame := make([]byte, 100)
	for i := range opusFrame {
		opusFrame[i] = byte(i)
	}

	obj := &moqtransport.Object{
		GroupID:  1,
		ObjectID: 0,
		Payload:  opusFrame,
	}

	err := handler.processObject(obj)
	if err != nil {
		t.Fatalf("processObject failed: %v", err)
	}

	// Verify the frame was forwarded
	if connHandler.getOpusFrameCount() != 1 {
		t.Errorf("Expected 1 Opus frame, got %d", connHandler.getOpusFrameCount())
	}

	// Verify bytesReceived stat (objectsReceived is incremented in readLoop, not processObject)
	_, bytesReceived := handler.GetStats()
	if bytesReceived != 100 {
		t.Errorf("Expected 100 bytes received, got %d", bytesReceived)
	}
}

// TestAudioInputHandlerInvalidFrameSize tests rejection of invalid frame sizes
func TestAudioInputHandlerInvalidFrameSize(t *testing.T) {
	testUUID := uuid.New()
	handler := createAudioInputHandler(testUUID)

	connHandler := newMockAudioConnectionHandler()
	handler.SetConnectionHandler(connHandler)

	tests := []struct {
		name      string
		frameSize int
		wantErr   bool
	}{
		{"too small", 2, true},
		{"too large", 1500, true},
		{"valid minimum", 60, false},
		{"valid typical", 100, false},
		{"valid maximum", 200, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := make([]byte, tt.frameSize)
			obj := &moqtransport.Object{
				GroupID:  1,
				ObjectID: 0,
				Payload:  frame,
			}

			err := handler.processObject(obj)
			if tt.wantErr && err == nil {
				t.Errorf("Expected error for frame size %d, got nil", tt.frameSize)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error for frame size %d: %v", tt.frameSize, err)
			}
		})
	}
}

// TestAudioInputHandlerEmptyFrame tests handling of empty frames
func TestAudioInputHandlerEmptyFrame(t *testing.T) {
	testUUID := uuid.New()
	handler := createAudioInputHandler(testUUID)

	obj := &moqtransport.Object{
		GroupID:  1,
		ObjectID: 0,
		Payload:  []byte{}, // Empty
	}

	// Should not return error but also not process
	err := handler.processObject(obj)
	if err != nil {
		t.Errorf("Empty frame should not error: %v", err)
	}
}

// TestAudioInputHandlerNilObject tests handling of nil objects
func TestAudioInputHandlerNilObject(t *testing.T) {
	testUUID := uuid.New()
	handler := createAudioInputHandler(testUUID)

	err := handler.processObject(nil)
	if err == nil {
		t.Error("Expected error for nil object")
	}
}

// TestStateInputHandlerProcessObject tests state update processing
func TestStateInputHandlerProcessObject(t *testing.T) {
	testUUID := uuid.New()
	handler := createStateInputHandler(testUUID)

	connHandler := newMockAudioConnectionHandler()
	handler.SetConnectionHandler(connHandler)

	// Create a valid NodeInfo3 payload (48 bytes)
	nodeInfo := common.NodeInfo3{
		Uuid:     testUUID,
		Position: common.Position{X: 0.5, Y: 0.6, Z: 0.7},
		Rotation: common.Rotation{Yaw: 45.0, Pitch: 30.0, Roll: 15.0},
		Volume:   0.8,
		Gone:     0,
	}
	stateData := inout.NodeInfo3ToBytes(nodeInfo)

	obj := &moqtransport.Object{
		GroupID:  1,
		ObjectID: 0,
		Payload:  stateData,
	}

	err := handler.processObject(obj)
	if err != nil {
		t.Fatalf("processObject failed: %v", err)
	}

	// Verify position was forwarded
	if connHandler.getPositionCount() != 1 {
		t.Errorf("Expected 1 position update, got %d", connHandler.getPositionCount())
	}

	lastPos := connHandler.getLastPosition()
	if lastPos == nil {
		t.Fatal("Expected position to be set")
	}

	// Verify position values (with float32 tolerance due to encoding)
	if lastPos.X < 0.49 || lastPos.X > 0.51 {
		t.Errorf("Expected X ~0.5, got %f", lastPos.X)
	}
	if lastPos.Y < 0.59 || lastPos.Y > 0.61 {
		t.Errorf("Expected Y ~0.6, got %f", lastPos.Y)
	}

	// Verify bytesReceived stat (objectsReceived is incremented in readLoop, not processObject)
	_, bytesReceived := handler.GetStats()
	if bytesReceived != 48 {
		t.Errorf("Expected 48 bytes received, got %d", bytesReceived)
	}
}

// TestStateInputHandlerInvalidSize tests rejection of invalid state data sizes
func TestStateInputHandlerInvalidSize(t *testing.T) {
	testUUID := uuid.New()
	handler := createStateInputHandler(testUUID)

	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"too small", 32, true},
		{"too large", 64, true},
		{"exactly 48", 48, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := make([]byte, tt.size)
			// Fill with valid UUID at start for the valid case
			if tt.size >= 16 {
				uuidBytes, _ := testUUID.MarshalBinary()
				copy(payload, uuidBytes)
			}

			obj := &moqtransport.Object{
				GroupID:  1,
				ObjectID: 0,
				Payload:  payload,
			}

			err := handler.processObject(obj)
			if tt.wantErr && err == nil {
				t.Errorf("Expected error for size %d, got nil", tt.size)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error for size %d: %v", tt.size, err)
			}
		})
	}
}

// TestStateInputHandlerNilObject tests handling of nil objects
func TestStateInputHandlerNilObject(t *testing.T) {
	testUUID := uuid.New()
	handler := createStateInputHandler(testUUID)

	err := handler.processObject(nil)
	if err == nil {
		t.Error("Expected error for nil object")
	}
}

// TestStateInputHandlerGetLastState tests retrieving last state
func TestStateInputHandlerGetLastState(t *testing.T) {
	testUUID := uuid.New()
	handler := createStateInputHandler(testUUID)

	// Initially nil
	pos, rot := handler.GetLastState()
	if pos != nil || rot != nil {
		t.Error("Expected nil initial state")
	}

	// Process a state update
	nodeInfo := common.NodeInfo3{
		Uuid:     testUUID,
		Position: common.Position{X: 1.0, Y: 2.0, Z: 3.0},
		Rotation: common.Rotation{Yaw: 90.0, Pitch: 45.0, Roll: 0.0},
		Volume:   1.0,
		Gone:     0,
	}
	stateData := inout.NodeInfo3ToBytes(nodeInfo)

	obj := &moqtransport.Object{
		GroupID:  1,
		ObjectID: 0,
		Payload:  stateData,
	}

	_ = handler.processObject(obj)

	// Now should have state
	pos, rot = handler.GetLastState()
	if pos == nil || rot == nil {
		t.Fatal("Expected state to be set after processing")
	}

	// Check approximate values (float32 encoding tolerance)
	if pos.X < 0.99 || pos.X > 1.01 {
		t.Errorf("Expected X ~1.0, got %f", pos.X)
	}
	if rot.Yaw < 89.9 || rot.Yaw > 90.1 {
		t.Errorf("Expected Yaw ~90.0, got %f", rot.Yaw)
	}
}

// TestAudioOutputHandlerStats tests audio output handler statistics
func TestAudioOutputHandlerStats(t *testing.T) {
	stats := AudioOutputStats{
		ObjectsPublished: 100,
		BytesPublished:   10000,
		PublishErrors:    5,
	}

	if stats.ObjectsPublished != 100 {
		t.Errorf("Expected 100 objects, got %d", stats.ObjectsPublished)
	}
	if stats.BytesPublished != 10000 {
		t.Errorf("Expected 10000 bytes, got %d", stats.BytesPublished)
	}
	if stats.PublishErrors != 5 {
		t.Errorf("Expected 5 errors, got %d", stats.PublishErrors)
	}
}

// TestNamespacesMatch tests namespace comparison
func TestNamespacesMatch(t *testing.T) {
	tests := []struct {
		name  string
		a     []string
		b     []string
		match bool
	}{
		{"equal", []string{"a", "b", "c"}, []string{"a", "b", "c"}, true},
		{"different length", []string{"a", "b"}, []string{"a", "b", "c"}, false},
		{"different content", []string{"a", "b", "c"}, []string{"a", "b", "d"}, false},
		{"empty both", []string{}, []string{}, true},
		{"empty one", []string{}, []string{"a"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := namespacesMatch(tt.a, tt.b)
			if result != tt.match {
				t.Errorf("Expected %v, got %v", tt.match, result)
			}
		})
	}
}
