package statecache

import (
	"encoding/json"
	"fmt"
)

// JsonOp represents the key and tombstone flag parsed from a JSON operation.
// The bouncer only reads these two fields — the "value" field is opaque.
type JsonOp struct {
	Key       string `json:"key"`
	Tombstone bool   `json:"tombstone,omitempty"`
}

// ParseOps detects whether msg is a single operation (starts with '{')
// or a batch (starts with '['), and returns the parsed key/tombstone
// flags alongside the raw JSON bytes of each individual operation.
// For a single op, returns a slice of one.
func ParseOps(msg []byte) ([]JsonOp, []json.RawMessage, error) {
	if len(msg) == 0 {
		return nil, nil, fmt.Errorf("empty message")
	}

	// Skip leading whitespace to find first meaningful byte.
	firstByte := byte(0)
	for _, b := range msg {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			firstByte = b
			break
		}
	}

	if firstByte == '[' {
		// Batch: split into individual raw messages first.
		var rawOps []json.RawMessage
		if err := json.Unmarshal(msg, &rawOps); err != nil {
			return nil, nil, fmt.Errorf("invalid batch JSON: %w", err)
		}
		if len(rawOps) == 0 {
			return nil, nil, fmt.Errorf("empty batch")
		}
		ops := make([]JsonOp, len(rawOps))
		for i, raw := range rawOps {
			if err := json.Unmarshal(raw, &ops[i]); err != nil {
				return nil, nil, fmt.Errorf("invalid op at index %d: %w", i, err)
			}
			if ops[i].Key == "" {
				return nil, nil, fmt.Errorf("empty key at index %d", i)
			}
		}
		return ops, rawOps, nil
	}

	if firstByte == '{' {
		var op JsonOp
		if err := json.Unmarshal(msg, &op); err != nil {
			return nil, nil, fmt.Errorf("invalid op JSON: %w", err)
		}
		if op.Key == "" {
			return nil, nil, fmt.Errorf("empty key")
		}
		return []JsonOp{op}, []json.RawMessage{json.RawMessage(msg)}, nil
	}

	return nil, nil, fmt.Errorf("unexpected first byte %q: expected '{' or '['", firstByte)
}

// ExtractOpValue parses a single op message and returns the JSON-encoded
// bytes of its "value" field. Returns nil if the message is malformed,
// is a tombstone, or has no value field. The returned bytes are the
// raw JSON (e.g. `0.5`, `true`) — callers Unmarshal them into a target
// of the expected type.
func ExtractOpValue(msg []byte) []byte {
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return nil
	}
	return env.Value
}

// BuildOp constructs a single operation JSON: {"key":"...","value":...}
func BuildOp(key string, value interface{}) ([]byte, error) {
	m := map[string]interface{}{
		"key":   key,
		"value": value,
	}
	return json.Marshal(m)
}

// BuildTombstoneOp constructs a tombstone operation: {"key":"...","tombstone":true}
func BuildTombstoneOp(key string) ([]byte, error) {
	m := map[string]interface{}{
		"key":       key,
		"tombstone": true,
	}
	return json.Marshal(m)
}

// BuildBatch wraps individual operation bytes into a JSON array.
func BuildBatch(ops [][]byte) ([]byte, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("empty batch")
	}
	// Build the array manually to preserve the original JSON bytes.
	buf := make([]byte, 0, 256)
	buf = append(buf, '[')
	for i, op := range ops {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, op...)
	}
	buf = append(buf, ']')
	return buf, nil
}
