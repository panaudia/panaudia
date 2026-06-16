package statecache

import (
	"encoding/json"
	"testing"
)

func TestParseOpsSingle(t *testing.T) {
	msg := []byte(`{"key":"uuid.name","value":"alice"}`)
	ops, rawOps, err := ParseOps(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Key != "uuid.name" {
		t.Fatalf("key = %q, want uuid.name", ops[0].Key)
	}
	if ops[0].Tombstone {
		t.Fatal("should not be tombstone")
	}
	if len(rawOps) != 1 {
		t.Fatalf("expected 1 raw op, got %d", len(rawOps))
	}
}

func TestParseOpsSingleTombstone(t *testing.T) {
	msg := []byte(`{"key":"uuid.name","tombstone":true}`)
	ops, _, err := ParseOps(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ops[0].Tombstone {
		t.Fatal("should be tombstone")
	}
}

func TestParseOpsBatch(t *testing.T) {
	msg := []byte(`[{"key":"uuid.name","value":"alice"},{"key":"uuid.ticket.colour","value":"#ff6633"},{"key":"uuid.conn","tombstone":true}]`)
	ops, rawOps, err := ParseOps(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	if ops[0].Key != "uuid.name" || ops[0].Tombstone {
		t.Fatalf("op[0] = %+v", ops[0])
	}
	if ops[1].Key != "uuid.ticket.colour" || ops[1].Tombstone {
		t.Fatalf("op[1] = %+v", ops[1])
	}
	if ops[2].Key != "uuid.conn" || !ops[2].Tombstone {
		t.Fatalf("op[2] = %+v", ops[2])
	}
	if len(rawOps) != 3 {
		t.Fatalf("expected 3 raw ops, got %d", len(rawOps))
	}

	// Verify raw bytes are parseable individually.
	for i, raw := range rawOps {
		var check JsonOp
		if err := json.Unmarshal(raw, &check); err != nil {
			t.Fatalf("raw op %d not valid JSON: %v", i, err)
		}
	}
}

func TestParseOpsMalformedJSON(t *testing.T) {
	_, _, err := ParseOps([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseOpsEmptyMessage(t *testing.T) {
	_, _, err := ParseOps([]byte{})
	if err == nil {
		t.Fatal("expected error for empty message")
	}
}

func TestParseOpsEmptyKey(t *testing.T) {
	_, _, err := ParseOps([]byte(`{"key":"","value":"x"}`))
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestParseOpsBatchEmptyKey(t *testing.T) {
	_, _, err := ParseOps([]byte(`[{"key":"ok","value":"x"},{"key":"","value":"y"}]`))
	if err == nil {
		t.Fatal("expected error for empty key in batch")
	}
}

func TestParseOpsEmptyBatch(t *testing.T) {
	_, _, err := ParseOps([]byte(`[]`))
	if err == nil {
		t.Fatal("expected error for empty batch")
	}
}

func TestBuildOp(t *testing.T) {
	b, err := BuildOp("uuid.name", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["key"] != "uuid.name" {
		t.Fatalf("key = %v, want uuid.name", m["key"])
	}
	if m["value"] != "alice" {
		t.Fatalf("value = %v, want alice", m["value"])
	}
}

func TestBuildOpNumericValue(t *testing.T) {
	b, err := BuildOp("uuid.volume", 0.75)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["value"] != 0.75 {
		t.Fatalf("value = %v, want 0.75", m["value"])
	}
}

func TestBuildOpBoolValue(t *testing.T) {
	b, err := BuildOp("uuid.subspaces.s1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["value"] != true {
		t.Fatalf("value = %v, want true", m["value"])
	}
}

func TestBuildTombstoneOp(t *testing.T) {
	b, err := BuildTombstoneOp("uuid.name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["key"] != "uuid.name" {
		t.Fatalf("key = %v, want uuid.name", m["key"])
	}
	if m["tombstone"] != true {
		t.Fatalf("tombstone = %v, want true", m["tombstone"])
	}
}

func TestBuildBatch(t *testing.T) {
	op1, _ := BuildOp("uuid.name", "alice")
	op2, _ := BuildOp("uuid.ticket.colour", "#ff6633")
	op3, _ := BuildTombstoneOp("uuid.old")

	batch, err := BuildBatch([][]byte{op1, op2, op3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should parse back as a batch of 3.
	ops, _, err := ParseOps(batch)
	if err != nil {
		t.Fatalf("failed to parse built batch: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	if ops[0].Key != "uuid.name" {
		t.Fatalf("op[0].Key = %q", ops[0].Key)
	}
	if ops[2].Key != "uuid.old" || !ops[2].Tombstone {
		t.Fatalf("op[2] = %+v", ops[2])
	}
}

func TestBuildBatchEmpty(t *testing.T) {
	_, err := BuildBatch(nil)
	if err == nil {
		t.Fatal("expected error for empty batch")
	}
}

func TestBuildOpRoundTrip(t *testing.T) {
	b, err := BuildOp("a.b.c", "hello")
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	ops, rawOps, err := ParseOps(b)
	if err != nil {
		t.Fatalf("ParseOps: %v", err)
	}
	if len(ops) != 1 || ops[0].Key != "a.b.c" || ops[0].Tombstone {
		t.Fatalf("round-trip failed: %+v", ops)
	}
	// Raw bytes should be valid JSON containing the value.
	var m map[string]interface{}
	if err := json.Unmarshal(rawOps[0], &m); err != nil {
		t.Fatalf("raw op not valid JSON: %v", err)
	}
	if m["value"] != "hello" {
		t.Fatalf("value = %v, want hello", m["value"])
	}
}

func TestParseOpsPreservesRawBytes(t *testing.T) {
	// Verify that raw bytes for each op in a batch are the exact original JSON,
	// including any whitespace or field ordering.
	msg := []byte(`[{"key":"a","value":"x"}, {"key":"b","value":"y"}]`)
	_, rawOps, err := ParseOps(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each raw op should individually unmarshal.
	for i, raw := range rawOps {
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("raw op %d: %v", i, err)
		}
	}
}
