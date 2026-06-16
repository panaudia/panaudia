package moq

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
)

// Track namespace constants
const (
	// Input track prefixes (client publishes, server subscribes)
	TrackNamespaceInAudio   = "in/audio/opus-mono"
	TrackNamespaceInState   = "state"
	TrackNamespaceInControl = "in/control"

	// Output track prefixes (server publishes, client subscribes)
	TrackNamespaceOutAudio      = "out/audio/opus-stereo"
	TrackNamespaceOutState      = "out/state"
	TrackNamespaceOutAttributes = "out/attributes"
	TrackNamespaceOutEntity     = "out/entity"
)

// TrackNames holds the track names for a specific node
type TrackNames struct {
	// Input tracks (client → server)
	AudioInputNamespace   []string // e.g., ["in", "audio", "opus-mono", "{nodeId}"]
	StateInputNamespace   []string // e.g., ["state", "{nodeId}"]
	ControlInputNamespace []string // e.g., ["in", "control", "{nodeId}"]

	// Output tracks (server → client)
	AudioOutputNamespace      []string // e.g., ["out", "audio", "opus-stereo", "{nodeId}"]
	StateOutputNamespace      []string // e.g., ["out", "state", "{nodeId}"]
	AttributesOutputNamespace []string // e.g., ["out", "attributes", "{nodeId}"]
	EntityOutputNamespace     []string // e.g., ["out", "entity", "{nodeId}"]
	SpaceOutputNamespace      []string // e.g., ["out", "space", "{nodeId}"]

	// Node ID
	NodeID string
}

// GenerateTrackNames creates track names for a given NodeConfig
func GenerateTrackNames(nodeConfig common.NodeConfig) TrackNames {
	nodeID := nodeConfig.Uuid.String()

	return TrackNames{
		// Input tracks
		AudioInputNamespace:   []string{"in", "audio", "opus-mono", nodeID},
		StateInputNamespace:   []string{"state", nodeID},
		ControlInputNamespace: []string{"in", "control", nodeID},

		// Output tracks
		AudioOutputNamespace:      []string{"out", "audio", "opus-stereo", nodeID},
		StateOutputNamespace:      []string{"out", "state", nodeID},
		AttributesOutputNamespace: []string{"out", "attributes", nodeID},
		EntityOutputNamespace:     []string{"out", "entity", nodeID},
		SpaceOutputNamespace:      []string{"out", "space", nodeID},

		NodeID: nodeID,
	}
}

// GenerateTrackNamesFromUUID creates track names from a UUID
func GenerateTrackNamesFromUUID(nodeUUID uuid.UUID) TrackNames {
	nodeID := nodeUUID.String()

	return TrackNames{
		AudioInputNamespace:       []string{"in", "audio", "opus-mono", nodeID},
		StateInputNamespace:       []string{"state", nodeID},
		ControlInputNamespace:     []string{"in", "control", nodeID},
		AudioOutputNamespace:      []string{"out", "audio", "opus-stereo", nodeID},
		StateOutputNamespace:      []string{"out", "state", nodeID},
		AttributesOutputNamespace: []string{"out", "attributes", nodeID},
		EntityOutputNamespace:     []string{"out", "entity", nodeID},
		SpaceOutputNamespace:      []string{"out", "space", nodeID},
		NodeID:                    nodeID,
	}
}

// AudioInputTrackName returns the full track name for audio input
func (t *TrackNames) AudioInputTrackName() string {
	return fmt.Sprintf("/%s", joinNamespace(t.AudioInputNamespace))
}

// StateInputTrackName returns the full track name for state input
func (t *TrackNames) StateInputTrackName() string {
	return fmt.Sprintf("/%s", joinNamespace(t.StateInputNamespace))
}

// AudioOutputTrackName returns the full track name for audio output
func (t *TrackNames) AudioOutputTrackName() string {
	return fmt.Sprintf("/%s", joinNamespace(t.AudioOutputNamespace))
}

// joinNamespace joins namespace components with slashes
func joinNamespace(namespace []string) string {
	if len(namespace) == 0 {
		return ""
	}

	result := namespace[0]
	for i := 1; i < len(namespace); i++ {
		result += "/" + namespace[i]
	}
	return result
}

// ParseTrackNamespace extracts the node ID from a track namespace
func ParseTrackNamespace(namespace []string) (nodeID string, trackType string, err error) {
	if len(namespace) == 0 {
		return "", "", fmt.Errorf("empty namespace")
	}

	// Check track type based on namespace pattern
	if len(namespace) >= 4 && namespace[0] == "in" && namespace[1] == "audio" && namespace[2] == "opus-mono" {
		// Audio input: ["in", "audio", "opus-mono", "{nodeId}"]
		return namespace[3], "audio-input", nil
	}

	if len(namespace) >= 4 && namespace[0] == "out" && namespace[1] == "audio" && namespace[2] == "opus-stereo" {
		// Audio output: ["out", "audio", "opus-stereo", "{nodeId}"]
		return namespace[3], "audio-output", nil
	}

	if len(namespace) >= 2 && namespace[0] == "state" {
		// State input: ["state", "{nodeId}"]
		return namespace[1], "state-input", nil
	}

	if len(namespace) >= 3 && namespace[0] == "out" && namespace[1] == "state" {
		// State output: ["out", "state", "{nodeId}"]
		return namespace[2], "state-output", nil
	}

	if len(namespace) >= 3 && namespace[0] == "out" && namespace[1] == "attributes" {
		// Attributes output: ["out", "attributes", "{nodeId}"]
		return namespace[2], "attributes-output", nil
	}

	if len(namespace) >= 3 && namespace[0] == "out" && namespace[1] == "entity" {
		// Entity output: ["out", "entity", "{nodeId}"]
		return namespace[2], "entity-output", nil
	}

	if len(namespace) >= 3 && namespace[0] == "in" && namespace[1] == "control" {
		// Control input: ["in", "control", "{nodeId}"]
		return namespace[2], "control-input", nil
	}

	return "", "", fmt.Errorf("unrecognized track namespace: %v", namespace)
}

// ValidateTrackNamespace checks if a namespace matches expected patterns
func ValidateTrackNamespace(namespace []string, expectedNodeID string) error {
	nodeID, trackType, err := ParseTrackNamespace(namespace)
	if err != nil {
		return err
	}

	if nodeID != expectedNodeID {
		return fmt.Errorf("track namespace node ID %s does not match expected %s", nodeID, expectedNodeID)
	}

	common.LogDebug("Validated track namespace: type=%s, nodeID=%s", trackType, nodeID)
	return nil
}
