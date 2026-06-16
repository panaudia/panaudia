package moq

import (
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
)

func TestGenerateTrackNames(t *testing.T) {
	testUUID := uuid.New()
	nodeConfig := common.NodeConfig{
		Uuid: testUUID,
		Name: "TestNode",
	}

	tracks := GenerateTrackNames(nodeConfig)

	// Check node ID
	if tracks.NodeID != testUUID.String() {
		t.Errorf("Expected NodeID %s, got %s", testUUID.String(), tracks.NodeID)
	}

	// Check audio input namespace
	expectedAudioIn := []string{"in", "audio", "opus-mono", testUUID.String()}
	if !sliceEqual(tracks.AudioInputNamespace, expectedAudioIn) {
		t.Errorf("Expected audio input namespace %v, got %v", expectedAudioIn, tracks.AudioInputNamespace)
	}

	// Check state input namespace
	expectedStateIn := []string{"state", testUUID.String()}
	if !sliceEqual(tracks.StateInputNamespace, expectedStateIn) {
		t.Errorf("Expected state input namespace %v, got %v", expectedStateIn, tracks.StateInputNamespace)
	}

	// Check audio output namespace
	expectedAudioOut := []string{"out", "audio", "opus-stereo", testUUID.String()}
	if !sliceEqual(tracks.AudioOutputNamespace, expectedAudioOut) {
		t.Errorf("Expected audio output namespace %v, got %v", expectedAudioOut, tracks.AudioOutputNamespace)
	}
}

func TestTrackNames_TrackName(t *testing.T) {
	testUUID := uuid.New()
	tracks := GenerateTrackNamesFromUUID(testUUID)

	// Test audio input track name
	audioIn := tracks.AudioInputTrackName()
	expectedAudioIn := "/in/audio/opus-mono/" + testUUID.String()
	if audioIn != expectedAudioIn {
		t.Errorf("Expected audio input track name %s, got %s", expectedAudioIn, audioIn)
	}

	// Test state input track name
	stateIn := tracks.StateInputTrackName()
	expectedStateIn := "/state/" + testUUID.String()
	if stateIn != expectedStateIn {
		t.Errorf("Expected state input track name %s, got %s", expectedStateIn, stateIn)
	}

	// Test audio output track name
	audioOut := tracks.AudioOutputTrackName()
	expectedAudioOut := "/out/audio/opus-stereo/" + testUUID.String()
	if audioOut != expectedAudioOut {
		t.Errorf("Expected audio output track name %s, got %s", expectedAudioOut, audioOut)
	}
}

func TestParseTrackNamespace(t *testing.T) {
	testUUID := uuid.New().String()

	tests := []struct {
		name         string
		namespace    []string
		expectedID   string
		expectedType string
		expectError  bool
	}{
		{
			name:         "audio input",
			namespace:    []string{"in", "audio", "opus-mono", testUUID},
			expectedID:   testUUID,
			expectedType: "audio-input",
			expectError:  false,
		},
		{
			name:         "state input",
			namespace:    []string{"state", testUUID},
			expectedID:   testUUID,
			expectedType: "state-input",
			expectError:  false,
		},
		{
			name:         "audio output",
			namespace:    []string{"out", "audio", "opus-stereo", testUUID},
			expectedID:   testUUID,
			expectedType: "audio-output",
			expectError:  false,
		},
		{
			name:        "empty namespace",
			namespace:   []string{},
			expectError: true,
		},
		{
			name:        "invalid namespace",
			namespace:   []string{"invalid", "namespace"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeID, trackType, err := ParseTrackNamespace(tt.namespace)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if nodeID != tt.expectedID {
				t.Errorf("Expected node ID %s, got %s", tt.expectedID, nodeID)
			}

			if trackType != tt.expectedType {
				t.Errorf("Expected track type %s, got %s", tt.expectedType, trackType)
			}
		})
	}
}

func TestValidateTrackNamespace(t *testing.T) {
	testUUID := uuid.New().String()
	otherUUID := uuid.New().String()

	tests := []struct {
		name        string
		namespace   []string
		expectedID  string
		expectError bool
	}{
		{
			name:        "valid audio input",
			namespace:   []string{"in", "audio", "opus-mono", testUUID},
			expectedID:  testUUID,
			expectError: false,
		},
		{
			name:        "valid state input",
			namespace:   []string{"state", testUUID},
			expectedID:  testUUID,
			expectError: false,
		},
		{
			name:        "mismatched node ID",
			namespace:   []string{"in", "audio", "opus-mono", otherUUID},
			expectedID:  testUUID,
			expectError: true,
		},
		{
			name:        "invalid namespace",
			namespace:   []string{"invalid"},
			expectedID:  testUUID,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTrackNamespace(tt.namespace, tt.expectedID)

			if tt.expectError && err == nil {
				t.Error("Expected error, got nil")
			}

			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

// Helper function to compare string slices
func sliceEqual(a, b []string) bool {
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
