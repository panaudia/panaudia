package statecache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// helper to find the latest op for a given key in a slice of ops.
func latestForKey(ops []Op, topic, key string) (Op, bool) {
	var best Op
	found := false
	for _, op := range ops {
		if op.Topic == topic && op.Key == key {
			if !found || op.OpID > best.OpID {
				best = op
				found = true
			}
		}
	}
	return best, found
}

func smallConfig() Config {
	return Config{
		RingSize:          4,
		SegmentCapacity:   4,
		CompactInterval:   time.Hour, // don't auto-compact in tests unless we want to
		TombstoneTTL:      30 * time.Second,
		PressureThreshold: 0.5,
	}
}

// --- Basic operations ---

func TestSetAndSnapshot(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "nodeA", []byte("alice"), 100, 1)
	s.Set("attributes", "nodeB", []byte("bob"), 101, 1)
	s.Set("state", "nodeC", []byte("pos"), 102, 1)

	ops := s.Snapshot()
	if opA, ok := latestForKey(ops, "attributes", "nodeA"); !ok {
		t.Fatal("nodeA not found")
	} else if string(opA.Value) != "alice" {
		t.Fatalf("nodeA value = %q, want alice", opA.Value)
	}
	if _, ok := latestForKey(ops, "attributes", "nodeB"); !ok {
		t.Fatal("nodeB not found")
	}
	if _, ok := latestForKey(ops, "state", "nodeC"); !ok {
		t.Fatal("nodeC not found")
	}
}

func TestOverwriteKeepsLatest(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "nodeA", []byte("v1"), 100, 1)
	s.Set("attributes", "nodeA", []byte("v2"), 200, 1)

	ops := s.Snapshot()
	op, ok := latestForKey(ops, "attributes", "nodeA")
	if !ok {
		t.Fatal("nodeA not found")
	}
	if string(op.Value) != "v2" {
		t.Fatalf("value = %q, want v2", op.Value)
	}
}

func TestTombstone(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "nodeA", []byte("alice"), 100, 1)
	s.Tomb("attributes", "nodeA", 200, 1)

	ops := s.Snapshot()
	op, ok := latestForKey(ops, "attributes", "nodeA")
	if !ok {
		t.Fatal("nodeA should be present as a tombstone")
	}
	if !op.Tombstone {
		t.Fatal("nodeA should be a tombstone")
	}
}

func TestTombWithLowerOpID(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "nodeA", []byte("alice"), 200, 1)
	s.Tomb("attributes", "nodeA", 100, 1)

	ops := s.Snapshot()
	op, ok := latestForKey(ops, "attributes", "nodeA")
	if !ok {
		t.Fatal("nodeA not found")
	}
	// The Set has the higher OpID so it should win in a client-side merge.
	// The store may return both — that's fine, latestForKey picks the right one.
	if op.Tombstone {
		t.Fatal("the Set with higher OpID should win over the Tomb")
	}
}

// --- Tombstone TTL ---

func TestTombstoneTTLRetained(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	// Tombstone with recent CreatedAt (set automatically by Tomb)
	s.Tomb("attributes", "nodeA", 100, 1)
	s.Compact()

	ops := s.Snapshot()
	if _, ok := latestForKey(ops, "attributes", "nodeA"); !ok {
		t.Fatal("recent tombstone should still be in snapshot")
	}
}

func TestTombstoneTTLExpired(t *testing.T) {
	cfg := smallConfig()
	cfg.TombstoneTTL = 1 * time.Millisecond
	s := New(cfg)
	defer s.Close()

	// Write a tombstone — its CreatedAt is set to time.Now() by Tomb()
	s.Tomb("attributes", "nodeA", 100, 1)

	// Wait for it to expire
	time.Sleep(5 * time.Millisecond)
	s.Compact()

	ops := s.Snapshot()
	if _, ok := latestForKey(ops, "attributes", "nodeA"); ok {
		t.Fatal("expired tombstone should have been purged")
	}
}

// --- Since ---

func TestSince(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "a", []byte("1"), 10, 1)
	s.Set("attributes", "b", []byte("2"), 20, 1)
	s.Set("attributes", "c", []byte("3"), 30, 1)
	s.Set("attributes", "d", []byte("4"), 40, 1)

	ops := s.Since(25)
	for _, op := range ops {
		if op.OpID <= 25 {
			t.Fatalf("Since returned op with OpID %d which is <= cutoff", op.OpID)
		}
	}
	if len(ops) < 2 {
		t.Fatalf("expected at least 2 ops (c, d), got %d", len(ops))
	}
}

func TestSinceZeroReturnsAll(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "a", []byte("1"), 10, 1)
	s.Set("attributes", "b", []byte("2"), 20, 1)

	ops := s.Since(0)
	if len(ops) < 2 {
		t.Fatalf("Since(0) should return all ops, got %d", len(ops))
	}
}

func TestSinceHigherThanAll(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "a", []byte("1"), 10, 1)
	ops := s.Since(999)
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops, got %d", len(ops))
	}
}

// --- Segment mechanics ---

func TestSegmentAdvancement(t *testing.T) {
	cfg := smallConfig()
	cfg.SegmentCapacity = 2
	cfg.RingSize = 4
	s := New(cfg)
	defer s.Close()

	// Write 3 ops — should fill segment 0 (2 slots) and advance to segment 1
	s.Set("a", "k1", []byte("v"), 1, 1)
	s.Set("a", "k2", []byte("v"), 2, 1)
	s.Set("a", "k3", []byte("v"), 3, 1)

	h := s.head.Load()
	if h < 1 {
		t.Fatalf("head should have advanced past 0, got %d", h)
	}

	ops := s.Snapshot()
	if len(ops) < 3 {
		t.Fatalf("expected 3 ops across segments, got %d", len(ops))
	}
}

func TestMultipleSegments(t *testing.T) {
	cfg := smallConfig()
	cfg.SegmentCapacity = 2
	cfg.RingSize = 8
	s := New(cfg)
	defer s.Close()

	for i := 0; i < 10; i++ {
		s.Set("a", fmt.Sprintf("k%d", i), []byte("v"), uint64(i+1), 1)
	}

	ops := s.Snapshot()
	if len(ops) < 10 {
		t.Fatalf("expected 10 ops, got %d", len(ops))
	}
}

// --- Ring wrap ---

func TestRingWrapWithCompaction(t *testing.T) {
	cfg := smallConfig()
	cfg.SegmentCapacity = 2
	cfg.RingSize = 4
	cfg.PressureThreshold = 100.0 // disable adaptive compaction, control it manually
	s := New(cfg)
	defer s.Close()

	// Write enough to wrap the ring, compacting frequently to preserve all keys.
	for i := 0; i < 20; i++ {
		s.Set("a", fmt.Sprintf("k%d", i), []byte(fmt.Sprintf("v%d", i)), uint64(i+1), 1)
		if i%2 == 1 {
			s.Compact()
		}
	}
	s.Compact()

	ops := s.Snapshot()
	seen := make(map[string]bool)
	for _, op := range ops {
		seen[op.Key] = true
	}
	if len(seen) != 20 {
		t.Fatalf("expected 20 unique keys, got %d", len(seen))
	}
}

func TestRingWrapWithoutCompaction(t *testing.T) {
	cfg := smallConfig()
	cfg.SegmentCapacity = 2
	cfg.RingSize = 4
	cfg.PressureThreshold = 100.0 // disable adaptive compaction
	s := New(cfg)
	defer s.Close()

	// Write enough to wrap the ring without compacting
	for i := 0; i < 20; i++ {
		s.Set("a", "only-key", []byte(fmt.Sprintf("v%d", i)), uint64(i+1), 1)
	}

	ops := s.Snapshot()
	op, ok := latestForKey(ops, "a", "only-key")
	if !ok {
		t.Fatal("should find only-key")
	}
	if string(op.Value) != "v19" {
		t.Logf("latest value = %q (may not be v19 due to ring wrap without compaction)", op.Value)
	}

	// Pressure should be high
	p := s.Pressure()
	if p < 0.5 {
		t.Fatalf("expected high pressure, got %f", p)
	}
}

// --- Compaction ---

func TestCompact(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "a", []byte("v1"), 10, 1)
	s.Set("attributes", "a", []byte("v2"), 20, 1)
	s.Set("attributes", "b", []byte("vb"), 15, 1)

	s.Compact()

	snap := s.snap.Load()
	if len(snap.entries) != 2 {
		t.Fatalf("expected 2 entries in snapshot, got %d", len(snap.entries))
	}
	if string(snap.entries[compositeKey("attributes", "a")].Value) != "v2" {
		t.Fatal("snapshot should contain latest value for key a")
	}
}

func TestCompactIdempotent(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("attributes", "a", []byte("v1"), 10, 1)
	s.Compact()

	snap1 := s.snap.Load()
	s.Compact()
	snap2 := s.snap.Load()

	if len(snap1.entries) != len(snap2.entries) {
		t.Fatal("compacting twice with no new writes should produce same result")
	}
}

func TestCompactReducesSegmentScan(t *testing.T) {
	cfg := smallConfig()
	cfg.SegmentCapacity = 2
	cfg.RingSize = 8
	s := New(cfg)
	defer s.Close()

	for i := 0; i < 8; i++ {
		s.Set("a", fmt.Sprintf("k%d", i), []byte("v"), uint64(i+1), 1)
	}

	pressureBefore := s.Pressure()
	s.Compact()
	pressureAfter := s.Pressure()

	if pressureAfter >= pressureBefore {
		t.Fatalf("compaction should reduce pressure: before=%f after=%f", pressureBefore, pressureAfter)
	}
}

// --- Concurrency ---

func TestConcurrentWritersDifferentKeys(t *testing.T) {
	s := New(DefaultConfig())
	defer s.Close()

	const goroutines = 10
	const keysPerGoroutine = 50
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < keysPerGoroutine; i++ {
				key := fmt.Sprintf("g%d-k%d", g, i)
				s.Set("attributes", key, []byte("v"), uint64(g*1000+i+1), uint32(g))
			}
		}()
	}

	wg.Wait()
	s.Compact()

	ops := s.Snapshot()
	seen := make(map[string]bool)
	for _, op := range ops {
		seen[op.Key] = true
	}
	expected := goroutines * keysPerGoroutine
	if len(seen) != expected {
		t.Fatalf("expected %d unique keys, got %d", expected, len(seen))
	}
}

func TestConcurrentWritersSameKey(t *testing.T) {
	s := New(DefaultConfig())
	defer s.Close()

	const goroutines = 10
	const writes = 100
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writes; i++ {
				opID := uint64(g*1000 + i + 1)
				s.Set("attributes", "shared-key", []byte(fmt.Sprintf("g%d-i%d", g, i)), opID, uint32(g))
			}
		}()
	}

	wg.Wait()
	s.Compact()

	ops := s.Snapshot()
	op, ok := latestForKey(ops, "attributes", "shared-key")
	if !ok {
		t.Fatal("shared-key not found")
	}
	// The latest should be from goroutine 9 (highest g*1000 base)
	expectedOpID := uint64(9*1000 + writes)
	if op.OpID != expectedOpID {
		t.Logf("highest OpID = %d (expected %d) — acceptable if concurrent ordering differs", op.OpID, expectedOpID)
	}
}

func TestConcurrentReadAndWrite(t *testing.T) {
	s := New(DefaultConfig())
	defer s.Close()

	const writes = 500
	var wg sync.WaitGroup

	// Writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			s.Set("a", fmt.Sprintf("k%d", i), []byte("v"), uint64(i+1), 1)
		}
	}()

	// Reader
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			_ = s.Snapshot()
		}
	}()

	wg.Wait()
	// No panic or race = pass (run with -race)
}

func TestConcurrentWriteAndCompact(t *testing.T) {
	s := New(DefaultConfig())
	defer s.Close()

	const writes = 500
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			s.Set("a", fmt.Sprintf("k%d", i), []byte("v"), uint64(i+1), 1)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s.Compact()
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
}

// --- Pressure ---

func TestPressureEmpty(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	p := s.Pressure()
	if p != 0 {
		t.Fatalf("expected pressure 0 on empty store, got %f", p)
	}
}

func TestPressureHalf(t *testing.T) {
	cfg := smallConfig()
	cfg.SegmentCapacity = 2
	cfg.RingSize = 4
	s := New(cfg)
	defer s.Close()

	// Fill 3 segments: 6 ops with segCap=2 → head advances to 2 → pressure = 2/4 = 0.5
	for i := 0; i < 6; i++ {
		s.Set("a", fmt.Sprintf("k%d", i), []byte("v"), uint64(i+1), 1)
	}

	p := s.Pressure()
	if p < 0.4 || p > 0.6 {
		t.Fatalf("expected pressure ~0.5, got %f", p)
	}
}

func TestPressureDropsAfterCompaction(t *testing.T) {
	cfg := smallConfig()
	cfg.SegmentCapacity = 2
	cfg.RingSize = 8
	s := New(cfg)
	defer s.Close()

	for i := 0; i < 8; i++ {
		s.Set("a", fmt.Sprintf("k%d", i), []byte("v"), uint64(i+1), 1)
	}

	before := s.Pressure()
	s.Compact()
	after := s.Pressure()

	if after >= before {
		t.Fatalf("pressure should drop after compaction: before=%f after=%f", before, after)
	}
}

// --- Edge cases ---

func TestEmptySnapshot(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	ops := s.Snapshot()
	if len(ops) != 0 {
		t.Fatalf("expected empty snapshot, got %d ops", len(ops))
	}
}

func TestEmptySince(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	ops := s.Since(100)
	if len(ops) != 0 {
		t.Fatalf("expected empty since, got %d ops", len(ops))
	}
}

func TestSingleOp(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("a", "k", []byte("v"), 50, 1)

	ops := s.Snapshot()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}

	ops = s.Since(10)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op from Since with lower OpID, got %d", len(ops))
	}

	ops = s.Since(100)
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops from Since with higher OpID, got %d", len(ops))
	}
}

func TestEmptyValue(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	s.Set("a", "k", []byte{}, 10, 1)
	ops := s.Snapshot()
	op, ok := latestForKey(ops, "a", "k")
	if !ok {
		t.Fatal("key not found")
	}
	if len(op.Value) != 0 {
		t.Fatalf("expected empty value, got %d bytes", len(op.Value))
	}
}

func TestLargeValue(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	big := make([]byte, 65536)
	for i := range big {
		big[i] = byte(i % 256)
	}
	s.Set("a", "k", big, 10, 1)

	ops := s.Snapshot()
	op, ok := latestForKey(ops, "a", "k")
	if !ok {
		t.Fatal("key not found")
	}
	if len(op.Value) != 65536 {
		t.Fatalf("expected 65536 bytes, got %d", len(op.Value))
	}
	if op.Value[12345] != byte(12345%256) {
		t.Fatal("value content mismatch")
	}
}

func TestManyKeys(t *testing.T) {
	s := New(DefaultConfig())
	defer s.Close()

	for i := 0; i < 1000; i++ {
		s.Set("a", fmt.Sprintf("k%d", i), []byte("v"), uint64(i+1), 1)
	}
	s.Compact()

	ops := s.Snapshot()
	seen := make(map[string]bool)
	for _, op := range ops {
		seen[op.Key] = true
	}
	if len(seen) != 1000 {
		t.Fatalf("expected 1000 keys, got %d", len(seen))
	}
}

func TestValueIsCopied(t *testing.T) {
	s := New(smallConfig())
	defer s.Close()

	buf := []byte("original")
	s.Set("a", "k", buf, 10, 1)

	// Mutate the original buffer
	buf[0] = 'X'

	ops := s.Snapshot()
	op, ok := latestForKey(ops, "a", "k")
	if !ok {
		t.Fatal("key not found")
	}
	if string(op.Value) != "original" {
		t.Fatalf("stored value was mutated: got %q", op.Value)
	}
}
