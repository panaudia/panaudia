package inout

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
)

// TestNodeInfo3RoundTrip tests that NodeInfo3 can be serialized and deserialized correctly
func TestNodeInfo3RoundTrip(t *testing.T) {
	testUUID := uuid.New()

	original := common.NodeInfo3{
		Uuid: testUUID,
		Position: common.Position{
			X: 0.5,
			Y: 0.6,
			Z: 0.7,
		},
		Rotation: common.Rotation{
			Yaw:   45.0,
			Pitch: 30.0,
			Roll:  15.0,
		},
		Volume: 0.8,
		Gone:   0,
	}

	// Encode to bytes
	encoded := NodeInfo3ToBytes(original)

	// Verify size is exactly 48 bytes
	if len(encoded) != 48 {
		t.Fatalf("Expected 48 bytes, got %d", len(encoded))
	}

	// Decode back
	decoded := NodeInfo3FromBytes(encoded)

	// Verify UUID
	if decoded.Uuid != original.Uuid {
		t.Errorf("UUID mismatch: expected %s, got %s", original.Uuid, decoded.Uuid)
	}

	// Verify Position (with float32 precision tolerance)
	if !floatClose(decoded.Position.X, original.Position.X, 1e-6) {
		t.Errorf("Position.X mismatch: expected %f, got %f", original.Position.X, decoded.Position.X)
	}
	if !floatClose(decoded.Position.Y, original.Position.Y, 1e-6) {
		t.Errorf("Position.Y mismatch: expected %f, got %f", original.Position.Y, decoded.Position.Y)
	}
	if !floatClose(decoded.Position.Z, original.Position.Z, 1e-6) {
		t.Errorf("Position.Z mismatch: expected %f, got %f", original.Position.Z, decoded.Position.Z)
	}

	// Verify Rotation
	if !floatClose(decoded.Rotation.Yaw, original.Rotation.Yaw, 1e-5) {
		t.Errorf("Rotation.Yaw mismatch: expected %f, got %f", original.Rotation.Yaw, decoded.Rotation.Yaw)
	}
	if !floatClose(decoded.Rotation.Pitch, original.Rotation.Pitch, 1e-5) {
		t.Errorf("Rotation.Pitch mismatch: expected %f, got %f", original.Rotation.Pitch, decoded.Rotation.Pitch)
	}
	if !floatClose(decoded.Rotation.Roll, original.Rotation.Roll, 1e-5) {
		t.Errorf("Rotation.Roll mismatch: expected %f, got %f", original.Rotation.Roll, decoded.Rotation.Roll)
	}

	// Verify Volume
	if !floatClose(decoded.Volume, original.Volume, 1e-6) {
		t.Errorf("Volume mismatch: expected %f, got %f", original.Volume, decoded.Volume)
	}
}

// TestNodeInfo3ZeroValues tests encoding with zero values
func TestNodeInfo3ZeroValues(t *testing.T) {
	testUUID := uuid.New()

	original := common.NodeInfo3{
		Uuid: testUUID,
		Position: common.Position{
			X: 0.0,
			Y: 0.0,
			Z: 0.0,
		},
		Rotation: common.Rotation{
			Yaw:   0.0,
			Pitch: 0.0,
			Roll:  0.0,
		},
		Volume: 0.0,
		Gone:   0,
	}

	encoded := NodeInfo3ToBytes(original)
	decoded := NodeInfo3FromBytes(encoded)

	if decoded.Uuid != original.Uuid {
		t.Errorf("UUID mismatch")
	}
	if decoded.Position.X != 0 || decoded.Position.Y != 0 || decoded.Position.Z != 0 {
		t.Errorf("Position should be zero")
	}
	if decoded.Rotation.Yaw != 0 || decoded.Rotation.Pitch != 0 || decoded.Rotation.Roll != 0 {
		t.Errorf("Rotation should be zero")
	}
	if decoded.Volume != 0 {
		t.Errorf("Volume should be zero")
	}
}

// TestNodeInfo3MaxValues tests encoding with maximum typical values
func TestNodeInfo3MaxValues(t *testing.T) {
	testUUID := uuid.New()

	original := common.NodeInfo3{
		Uuid: testUUID,
		Position: common.Position{
			X: 1.0,
			Y: 1.0,
			Z: 1.0,
		},
		Rotation: common.Rotation{
			Yaw:   360.0,
			Pitch: 90.0,
			Roll:  180.0,
		},
		Volume: 1.0,
		Gone:   1,
	}

	encoded := NodeInfo3ToBytes(original)
	decoded := NodeInfo3FromBytes(encoded)

	if decoded.Uuid != original.Uuid {
		t.Errorf("UUID mismatch")
	}
	if !floatClose(decoded.Position.X, 1.0, 1e-6) {
		t.Errorf("Position.X mismatch")
	}
	if !floatClose(decoded.Rotation.Yaw, 360.0, 1e-5) {
		t.Errorf("Rotation.Yaw mismatch")
	}
	if !floatClose(decoded.Volume, 1.0, 1e-6) {
		t.Errorf("Volume mismatch")
	}
}

// TestNodeInfo3NegativeRotation tests encoding with negative rotation values
func TestNodeInfo3NegativeRotation(t *testing.T) {
	testUUID := uuid.New()

	original := common.NodeInfo3{
		Uuid: testUUID,
		Position: common.Position{
			X: 0.5,
			Y: 0.5,
			Z: 0.5,
		},
		Rotation: common.Rotation{
			Yaw:   -45.0,
			Pitch: -30.0,
			Roll:  -15.0,
		},
		Volume: 0.5,
		Gone:   0,
	}

	encoded := NodeInfo3ToBytes(original)
	decoded := NodeInfo3FromBytes(encoded)

	if !floatClose(decoded.Rotation.Yaw, -45.0, 1e-5) {
		t.Errorf("Rotation.Yaw mismatch: expected -45.0, got %f", decoded.Rotation.Yaw)
	}
	if !floatClose(decoded.Rotation.Pitch, -30.0, 1e-5) {
		t.Errorf("Rotation.Pitch mismatch: expected -30.0, got %f", decoded.Rotation.Pitch)
	}
	if !floatClose(decoded.Rotation.Roll, -15.0, 1e-5) {
		t.Errorf("Rotation.Roll mismatch: expected -15.0, got %f", decoded.Rotation.Roll)
	}
}

// TestNodeInfo3ByteSize verifies the exact byte layout
func TestNodeInfo3ByteSize(t *testing.T) {
	testUUID := uuid.New()
	info := common.NodeInfo3{
		Uuid:     testUUID,
		Position: common.Position{X: 0.5, Y: 0.6, Z: 0.7},
		Rotation: common.Rotation{Yaw: 45.0, Pitch: 30.0, Roll: 15.0},
		Volume:   0.8,
		Gone:     0,
	}

	encoded := NodeInfo3ToBytes(info)

	// Verify total size
	if len(encoded) != 48 {
		t.Fatalf("Expected 48 bytes, got %d", len(encoded))
	}

	// Verify UUID is at bytes 0-15
	uuidBytes, _ := testUUID.MarshalBinary()
	for i := 0; i < 16; i++ {
		if encoded[i] != uuidBytes[i] {
			t.Errorf("UUID byte %d mismatch: expected %d, got %d", i, uuidBytes[i], encoded[i])
		}
	}
}

// BenchmarkNodeInfo3ToBytes benchmarks encoding performance
func BenchmarkNodeInfo3ToBytes(b *testing.B) {
	testUUID := uuid.New()
	info := common.NodeInfo3{
		Uuid:     testUUID,
		Position: common.Position{X: 0.5, Y: 0.6, Z: 0.7},
		Rotation: common.Rotation{Yaw: 45.0, Pitch: 30.0, Roll: 15.0},
		Volume:   0.8,
		Gone:     0,
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		_ = NodeInfo3ToBytes(info)
	}
}

// BenchmarkNodeInfo3FromBytes benchmarks decoding performance
func BenchmarkNodeInfo3FromBytes(b *testing.B) {
	testUUID := uuid.New()
	info := common.NodeInfo3{
		Uuid:     testUUID,
		Position: common.Position{X: 0.5, Y: 0.6, Z: 0.7},
		Rotation: common.Rotation{Yaw: 45.0, Pitch: 30.0, Roll: 15.0},
		Volume:   0.8,
		Gone:     0,
	}
	encoded := NodeInfo3ToBytes(info)

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		_ = NodeInfo3FromBytes(encoded)
	}
}

// floatClose checks if two floats are approximately equal
func floatClose(a, b, tolerance float64) bool {
	return math.Abs(a-b) < tolerance
}
