package statecache

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// testVector is a single test case shared across Go, TypeScript, and C++.
type testVector struct {
	Name      string `json:"name"`
	Topic     string `json:"topic"`
	Key       string `json:"key"`
	ValueHex  string `json:"value_hex"`
	OpID      uint64 `json:"op_id"`
	NodeID    uint32 `json:"node_id"`
	Tombstone bool   `json:"tombstone"`
	Encoded   string `json:"encoded_hex"`
}

func makeTestVectors() []testVector {
	return []testVector{
		{
			Name:      "normal_set",
			Topic:     "attributes",
			Key:       "abc-123",
			ValueHex:  hex.EncodeToString([]byte(`{"name":"alice"}`)),
			OpID:      65536005,
			NodeID:    42,
			Tombstone: false,
		},
		{
			Name:      "tombstone",
			Topic:     "attributes",
			Key:       "abc-123",
			ValueHex:  "",
			OpID:      131072000,
			NodeID:    42,
			Tombstone: true,
		},
		{
			Name:      "empty_value",
			Topic:     "state",
			Key:       "node-1",
			ValueHex:  "",
			OpID:      32768001,
			NodeID:    1,
			Tombstone: false,
		},
		{
			Name:      "max_key",
			Topic:     "attributes",
			Key:       string(bytes.Repeat([]byte("K"), 255)),
			ValueHex:  hex.EncodeToString([]byte("v")),
			OpID:      655359999,
			NodeID:    0xFFFFFFFF,
			Tombstone: false,
		},
		{
			Name:      "all_zero_value",
			Topic:     "t",
			Key:       "k",
			ValueHex:  hex.EncodeToString(make([]byte, 8)),
			OpID:      65536,
			NodeID:    0,
			Tombstone: false,
		},
		{
			Name:      "all_ff_value",
			Topic:     "t",
			Key:       "k",
			ValueHex:  hex.EncodeToString(bytes.Repeat([]byte{0xFF}, 8)),
			OpID:      65536,
			NodeID:    0,
			Tombstone: false,
		},
	}
}

func vectorToOp(v testVector) Op {
	val, _ := hex.DecodeString(v.ValueHex)
	return Op{
		Topic:     v.Topic,
		Key:       v.Key,
		Value:     val,
		OpID:      v.OpID,
		NodeID:    v.NodeID,
		Tombstone: v.Tombstone,
	}
}

// TestGenerateTestVectors encodes all vectors and writes the JSON file.
// Run this once; the file is then used by TS and C++ decoders.
func TestGenerateTestVectors(t *testing.T) {
	vectors := makeTestVectors()
	for i := range vectors {
		op := vectorToOp(vectors[i])
		encoded, err := Encode(op)
		if err != nil {
			t.Fatalf("encode %s: %v", vectors[i].Name, err)
		}
		vectors[i].Encoded = hex.EncodeToString(encoded)
	}

	data, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/wire_vectors.json", data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d vectors to testdata/wire_vectors.json", len(vectors))
}

// TestRoundTrip encodes then decodes each vector and verifies all fields.
func TestRoundTrip(t *testing.T) {
	vectors := makeTestVectors()
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			op := vectorToOp(v)
			encoded, err := Encode(op)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if decoded.Topic != op.Topic {
				t.Errorf("topic: got %q, want %q", decoded.Topic, op.Topic)
			}
			if decoded.Key != op.Key {
				t.Errorf("key: got %q, want %q", decoded.Key, op.Key)
			}
			if !bytes.Equal(decoded.Value, op.Value) {
				t.Errorf("value: got %x, want %x", decoded.Value, op.Value)
			}
			if decoded.OpID != op.OpID {
				t.Errorf("opID: got %d, want %d", decoded.OpID, op.OpID)
			}
			if decoded.NodeID != op.NodeID {
				t.Errorf("nodeID: got %d, want %d", decoded.NodeID, op.NodeID)
			}
			if decoded.Tombstone != op.Tombstone {
				t.Errorf("tombstone: got %v, want %v", decoded.Tombstone, op.Tombstone)
			}
		})
	}
}

// TestRoundTripTombstone specifically tests tombstone flag preservation.
func TestRoundTripTombstone(t *testing.T) {
	op := Op{Topic: "a", Key: "k", OpID: 100, NodeID: 1, Tombstone: true}
	encoded, err := Encode(op)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Tombstone {
		t.Fatal("tombstone flag lost")
	}
	if decoded.Value != nil {
		t.Fatalf("tombstone value should be nil, got %v", decoded.Value)
	}
}

// TestRoundTripEmptyValue tests a Set with zero-length value.
func TestRoundTripEmptyValue(t *testing.T) {
	op := Op{Topic: "a", Key: "k", Value: []byte{}, OpID: 100, NodeID: 1}
	encoded, err := Encode(op)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Value) != 0 {
		t.Fatalf("expected empty value, got %d bytes", len(decoded.Value))
	}
}

// TestRoundTripLargeValue tests with a 64KB payload.
func TestRoundTripLargeValue(t *testing.T) {
	big := make([]byte, 65536)
	for i := range big {
		big[i] = byte(i % 256)
	}
	op := Op{Topic: "a", Key: "k", Value: big, OpID: 100, NodeID: 1}
	encoded, err := Encode(op)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Value, big) {
		t.Fatal("large value mismatch")
	}
}

// TestDecodeTruncated verifies error on input shorter than minimum.
func TestDecodeTruncated(t *testing.T) {
	_, err := Decode([]byte{0xCA, 0x00})
	if err == nil {
		t.Fatal("expected error for truncated input")
	}
}

// TestDecodeZeroLength verifies error on empty input.
func TestDecodeZeroLength(t *testing.T) {
	_, err := Decode([]byte{})
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

// TestDecodeWrongVersion verifies error with clear message.
func TestDecodeWrongVersion(t *testing.T) {
	buf := make([]byte, minWireLen)
	buf[0] = 0x01 // wrong version
	_, err := Decode(buf)
	if err == nil {
		t.Fatal("expected error for wrong version")
	}
	if !isUnsupportedVersion(err) {
		t.Fatalf("expected unsupported version error, got: %v", err)
	}
}

func isUnsupportedVersion(err error) bool {
	return err != nil && err.Error() != "" && bytes.Contains([]byte(err.Error()), []byte("unsupported"))
}

// TestDecodeKeyOverrun verifies error when key length exceeds remaining bytes.
func TestDecodeKeyOverrun(t *testing.T) {
	op := Op{Topic: "a", Key: "k", Value: []byte("v"), OpID: 100, NodeID: 1}
	encoded, _ := Encode(op)
	// Corrupt the key length to be much larger
	topicLen := int(encoded[14])
	keyLenOff := 15 + topicLen
	encoded[keyLenOff] = 0xFF
	encoded[keyLenOff+1] = 0xFF

	_, err := Decode(encoded)
	if err == nil {
		t.Fatal("expected error for key overrun")
	}
}

// TestDecodeValueOverrun verifies error when value length exceeds remaining bytes.
func TestDecodeValueOverrun(t *testing.T) {
	op := Op{Topic: "a", Key: "k", Value: []byte("v"), OpID: 100, NodeID: 1}
	encoded, _ := Encode(op)
	// Corrupt the value length
	topicLen := int(encoded[14])
	keyLenOff := 15 + topicLen
	keyLen := int(encoded[keyLenOff])<<8 | int(encoded[keyLenOff+1])
	valLenOff := keyLenOff + 2 + keyLen
	encoded[valLenOff] = 0xFF
	encoded[valLenOff+1] = 0xFF
	encoded[valLenOff+2] = 0xFF
	encoded[valLenOff+3] = 0xFF

	_, err := Decode(encoded)
	if err == nil {
		t.Fatal("expected error for value overrun")
	}
}

// TestFieldBoundaries encodes with sentinel values and verifies no bleed.
func TestFieldBoundaries(t *testing.T) {
	key := string(bytes.Repeat([]byte{0xAA}, 10))
	value := bytes.Repeat([]byte{0xBB}, 10)
	op := Op{Topic: "topic", Key: key, Value: value, OpID: 0x1122334455667788, NodeID: 0xAABBCCDD}

	encoded, err := Encode(op)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.Key != key {
		t.Error("key field corrupted")
	}
	for _, b := range decoded.Value {
		if b != 0xBB {
			t.Error("value field corrupted — boundary bleed detected")
			break
		}
	}
	if decoded.OpID != 0x1122334455667788 {
		t.Error("OpID field corrupted")
	}
	if decoded.NodeID != 0xAABBCCDD {
		t.Error("NodeID field corrupted")
	}
}

// TestIsCacheEnvelope verifies the detection helper.
func TestIsCacheEnvelope(t *testing.T) {
	op := Op{Topic: "a", Key: "k", Value: []byte("v"), OpID: 100, NodeID: 1}
	encoded, _ := Encode(op)

	if !IsCacheEnvelope(encoded) {
		t.Fatal("should detect cache envelope")
	}
	if IsCacheEnvelope([]byte(`{"name":"alice"}`)) {
		t.Fatal("should not detect JSON as cache envelope")
	}
	if IsCacheEnvelope([]byte{}) {
		t.Fatal("should not detect empty as cache envelope")
	}
}

// TestVectorValidation encodes each test vector and compares against the saved hex.
func TestVectorValidation(t *testing.T) {
	data, err := os.ReadFile("testdata/wire_vectors.json")
	if err != nil {
		t.Skip("test vectors not generated yet; run TestGenerateTestVectors first")
	}

	var vectors []testVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}

	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			// Verify encoding produces expected bytes
			op := vectorToOp(v)
			encoded, err := Encode(op)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			gotHex := hex.EncodeToString(encoded)
			if gotHex != v.Encoded {
				t.Errorf("encoding mismatch:\n  got:  %s\n  want: %s", gotHex, v.Encoded)
			}

			// Verify decoding the expected bytes produces correct fields
			expectedBytes, _ := hex.DecodeString(v.Encoded)
			decoded, err := Decode(expectedBytes)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if decoded.Topic != v.Topic || decoded.Key != v.Key || decoded.OpID != v.OpID ||
				decoded.NodeID != v.NodeID || decoded.Tombstone != v.Tombstone {
				t.Error("decoded fields do not match vector")
			}
			expectedVal, _ := hex.DecodeString(v.ValueHex)
			if !bytes.Equal(decoded.Value, expectedVal) {
				t.Error("decoded value does not match vector")
			}
		})
	}
}
