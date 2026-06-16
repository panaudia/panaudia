package statecache

import (
	"testing"
)

func TestDefaultPolicyCachesAttributes(t *testing.T) {
	policy := DefaultPolicy()
	if !policy("attributes") {
		t.Fatal("default policy should cache 'attributes'")
	}
}

func TestDefaultPolicySkipsState(t *testing.T) {
	policy := DefaultPolicy()
	if policy("state") {
		t.Fatal("default policy should not cache 'state'")
	}
}

func TestDefaultPolicySkipsUnknown(t *testing.T) {
	policy := DefaultPolicy()
	if policy("other") {
		t.Fatal("default policy should not cache unknown topics")
	}
}

func TestCustomPolicy(t *testing.T) {
	policy := func(topic string) bool {
		return topic == "attributes" || topic == "custom-topic"
	}
	if !policy("attributes") {
		t.Fatal("custom policy should cache 'attributes'")
	}
	if !policy("custom-topic") {
		t.Fatal("custom policy should cache 'custom-topic'")
	}
	if policy("state") {
		t.Fatal("custom policy should not cache 'state'")
	}
}

func TestDefaultKeyExtractorAttributes(t *testing.T) {
	extractor := DefaultKeyExtractor()
	msg := []byte(`{"key":"abc-123.name","value":"alice"}`)
	key, tombstone, ok := extractor("attributes", msg)
	if !ok {
		t.Fatal("should extract key from valid op JSON")
	}
	if key != "abc-123.name" {
		t.Fatalf("key = %q, want abc-123.name", key)
	}
	if tombstone {
		t.Fatal("regular message should not be a tombstone")
	}
}

func TestDefaultKeyExtractorTombstone(t *testing.T) {
	extractor := DefaultKeyExtractor()
	msg := []byte(`{"key":"abc-123.name","tombstone":true}`)
	key, tombstone, ok := extractor("attributes", msg)
	if !ok {
		t.Fatal("should extract key from tombstone op JSON")
	}
	if key != "abc-123.name" {
		t.Fatalf("key = %q, want abc-123.name", key)
	}
	if !tombstone {
		t.Fatal("should be recognised as a tombstone")
	}
}

func TestDefaultKeyExtractorTombstoneFalse(t *testing.T) {
	extractor := DefaultKeyExtractor()
	msg := []byte(`{"key":"abc-123.name","tombstone":false}`)
	_, tombstone, ok := extractor("attributes", msg)
	if !ok {
		t.Fatal("should extract key")
	}
	if tombstone {
		t.Fatal("tombstone:false should not be a tombstone")
	}
}

func TestDefaultKeyExtractorMalformedJSON(t *testing.T) {
	extractor := DefaultKeyExtractor()
	msg := []byte(`not json`)
	_, _, ok := extractor("attributes", msg)
	if ok {
		t.Fatal("should fail on malformed JSON")
	}
}

func TestDefaultKeyExtractorMissingKey(t *testing.T) {
	extractor := DefaultKeyExtractor()
	msg := []byte(`{"value":"alice"}`)
	_, _, ok := extractor("attributes", msg)
	if ok {
		t.Fatal("should fail when key field is missing")
	}
}

func TestDefaultKeyExtractorEmptyKey(t *testing.T) {
	extractor := DefaultKeyExtractor()
	msg := []byte(`{"key":"","value":"alice"}`)
	_, _, ok := extractor("attributes", msg)
	if ok {
		t.Fatal("should fail when key is empty string")
	}
}

func TestDefaultKeyExtractorUnknownTopic(t *testing.T) {
	extractor := DefaultKeyExtractor()
	msg := []byte(`{"key":"abc-123.name","value":"alice"}`)
	_, _, ok := extractor("state", msg)
	if ok {
		t.Fatal("should fail for unknown topic")
	}
}

func TestOpIDCounterMonotonic(t *testing.T) {
	var counter OpIDCounter
	prev := uint64(0)
	for i := 0; i < 1000; i++ {
		id := counter.Assign()
		if id <= prev {
			t.Fatalf("opID %d is not greater than previous %d", id, prev)
		}
		prev = id
	}
}

func TestOpIDCounterStartsAtOne(t *testing.T) {
	var counter OpIDCounter
	if counter.Current() != 0 {
		t.Fatal("fresh counter should report 0")
	}
	id := counter.Assign()
	if id != 1 {
		t.Fatalf("first opID should be 1, got %d", id)
	}
	if counter.Current() != 1 {
		t.Fatalf("current should be 1 after first assign, got %d", counter.Current())
	}
}
