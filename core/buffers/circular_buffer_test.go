package buffers

import (
	"testing"
)

// Helper to make a config with simple sample rate for easy arithmetic.
// SampleRate=1000 means 1 sample per ms.
func testConfig() CircularBufferConfig {
	return CircularBufferConfig{
		SampleRate:         1000,
		NumChannels:        1,
		TargetLatencyMs:    20,
		TargetWindowMs:     6,  // TargetLow=17, TargetHigh=23
		MinLatencyMs:       3,
		MaxLatencyMs:       50,
		CapacityMs:         100,
		CorrectionInterval: 1000, // disabled by default
	}
}

func TestWriteBasic(t *testing.T) {
	b := NewCircularBuffer(testConfig())
	b.Write([]float32{1, 2, 3, 4})
	if b.buffered != 4 {
		t.Fatalf("expected buffered=4, got %d", b.buffered)
	}
	if b.writePos != 4 {
		t.Fatalf("expected writePos=4, got %d", b.writePos)
	}
}

func TestWriteWrapAround(t *testing.T) {
	cfg := testConfig()
	cfg.CapacityMs = 10 // 10 floats
	b := NewCircularBuffer(cfg)

	b.Write([]float32{1, 2, 3, 4, 5, 6, 7})
	if b.buffered != 7 {
		t.Fatalf("expected buffered=7, got %d", b.buffered)
	}

	// Write 5 more — wraps, overflow discards oldest
	b.Write([]float32{8, 9, 10, 11, 12})
	if b.buffered != 10 {
		t.Fatalf("expected buffered=10 (capped at capacity), got %d", b.buffered)
	}
	if b.writePos != 2 {
		t.Fatalf("expected writePos=2, got %d", b.writePos)
	}
}

func TestWriteOverflowDiscardsOldest(t *testing.T) {
	cfg := testConfig()
	cfg.CapacityMs = 8
	b := NewCircularBuffer(cfg)

	b.Write([]float32{1, 2, 3, 4, 5, 6, 7, 8})
	if b.buffered != 8 {
		t.Fatalf("expected buffered=8, got %d", b.buffered)
	}

	// Write 3 more — capacity is 8, so oldest 3 should be discarded
	b.Write([]float32{9, 10, 11})
	if b.buffered != 8 {
		t.Fatalf("expected buffered=8 after overflow, got %d", b.buffered)
	}
	// readPos should have advanced by 3
	if b.readPos != 3 {
		t.Fatalf("expected readPos=3 after overflow, got %d", b.readPos)
	}
}

func TestFillingState(t *testing.T) {
	b := NewCircularBuffer(testConfig())
	// TargetLow = 17 samples. State starts as FILLING.
	if b.state != stateFilling {
		t.Fatalf("expected initial state=filling, got %s", b.state)
	}

	dst := make([]float32, 4)

	// Write 10 — below TargetLow
	b.Write(make([]float32, 10))
	ok := b.Read(dst)
	if ok {
		t.Fatal("expected Read to return false while filling")
	}
	for i, v := range dst {
		if v != 0 {
			t.Fatalf("expected silence at dst[%d], got %f", i, v)
		}
	}
	if b.state != stateFilling {
		t.Fatalf("expected state=filling, got %s", b.state)
	}
}

func TestFillingToPlayingTransition(t *testing.T) {
	b := NewCircularBuffer(testConfig())
	// TargetLow = 17

	dst := make([]float32, 4)

	// Write exactly TargetLow samples
	b.Write(make([]float32, 17))
	ok := b.Read(dst)
	if !ok {
		t.Fatal("expected Read to return true after reaching TargetLow")
	}
	if b.state != statePlaying {
		t.Fatalf("expected state=playing, got %s", b.state)
	}
	// Should have consumed 4 floats
	if b.buffered != 13 {
		t.Fatalf("expected buffered=13, got %d", b.buffered)
	}
}

func TestUnderrunTransition(t *testing.T) {
	cfg := testConfig()
	cfg.MinLatencyMs = 5 // min = 5 samples
	b := NewCircularBuffer(cfg)
	// TargetLow = 17, Min = 5

	// Fill to TargetLow and transition to playing
	b.Write(make([]float32, 17))
	dst := make([]float32, 4)
	ok := b.Read(dst) // transitions to playing, buffered = 13
	if !ok || b.state != statePlaying {
		t.Fatal("should be playing after first read")
	}

	// Drain: reads of 4 → 13, 9, 5, 1...
	b.Read(dst) // buffered = 9
	b.Read(dst) // buffered = 5
	b.Read(dst) // fillSamples=5 >= min=5, reads 4, buffered = 1

	// Next read: fillSamples = 1 < min = 5 → underrun
	ok = b.Read(dst)
	if ok {
		t.Fatal("expected Read to return false on underrun")
	}
	if b.state != stateFilling {
		t.Fatalf("expected state=filling after underrun, got %s", b.state)
	}
	// Check silence output
	for i, v := range dst {
		if v != 0 {
			t.Fatalf("expected silence at dst[%d] on underrun, got %f", i, v)
		}
	}
}

func TestOverrunSnap(t *testing.T) {
	b := NewCircularBuffer(testConfig())
	// TargetCentre = 20, Max = 50

	// Get to playing state
	b.Write(make([]float32, 17))
	dst := make([]float32, 2)
	b.Read(dst) // playing, buffered = 15

	// Write a lot to trigger overrun
	b.Write(make([]float32, 50)) // buffered = 65, fillSamples = 65 > max (50)

	b.Read(dst) // should snap, then read
	// Snap sets buffered to targetCentre * nc = 20
	// Then reads 2, so buffered = 18
	if b.buffered != 18 {
		t.Fatalf("expected buffered=18 after overrun snap + read, got %d", b.buffered)
	}
	if b.overrunCount != 1 {
		t.Fatalf("expected overrunCount=1, got %d", b.overrunCount)
	}
}

func TestDriftCorrectionDrop(t *testing.T) {
	cfg := testConfig()
	cfg.CorrectionInterval = 1 // correct every read
	b := NewCircularBuffer(cfg)
	// TargetHigh = 23. Fill to 24 → above target → correction = +1

	b.Write(make([]float32, 24))
	dst := make([]float32, 4)
	b.Read(dst)
	// Should consume 4 + 1 = 5 floats
	if b.buffered != 19 {
		t.Fatalf("expected buffered=19 after drop correction, got %d", b.buffered)
	}
	if b.samplesDropped != 1 {
		t.Fatalf("expected samplesDropped=1, got %d", b.samplesDropped)
	}
}

func TestDriftCorrectionInsert(t *testing.T) {
	cfg := testConfig()
	cfg.CorrectionInterval = 1
	cfg.MinLatencyMs = 1
	b := NewCircularBuffer(cfg)
	// TargetLow=17. Need to get to PLAYING, then drain below TargetLow.

	// Write labeled data
	data := make([]float32, 17)
	for i := range data {
		data[i] = float32(i + 1)
	}
	b.Write(data)

	// First read: transitions to PLAYING, fillSamples=17 is at TargetLow boundary, no correction
	dst := make([]float32, 4)
	b.Read(dst) // buffered = 13

	// Second read: fillSamples=13 < TargetLow=17 → correction = -1
	dst2 := make([]float32, 4)
	b.Read(dst2)
	// realFloats = 4 + (-1)*1 = 3. Reads 3, duplicates last.
	if b.buffered != 10 { // 13 - 3 = 10
		t.Fatalf("expected buffered=10 after insert correction, got %d", b.buffered)
	}
	if b.samplesInserted != 1 {
		t.Fatalf("expected samplesInserted=1, got %d", b.samplesInserted)
	}
	// Verify duplication: dst2[3] should equal dst2[2]
	if dst2[3] != dst2[2] {
		t.Fatalf("expected duplicated sample: dst2[2]=%f, dst2[3]=%f", dst2[2], dst2[3])
	}
}

func TestCorrectionInterval(t *testing.T) {
	cfg := testConfig()
	cfg.CorrectionInterval = 3
	b := NewCircularBuffer(cfg)
	// TargetHigh = 23. Fill to 40 → above target → correction = +1 (but only every 3rd read)

	b.Write(make([]float32, 40))
	dst := make([]float32, 2)

	// Read 1: counter=1, no correction
	b.Read(dst)
	if b.buffered != 38 {
		t.Fatalf("read 1: expected buffered=38, got %d", b.buffered)
	}
	if b.samplesDropped != 0 {
		t.Fatalf("read 1: expected no correction")
	}

	// Read 2: counter=2, no correction
	b.Read(dst)
	if b.buffered != 36 {
		t.Fatalf("read 2: expected buffered=36, got %d", b.buffered)
	}

	// Read 3: counter=3, correction applied
	// fillSamples = 36 > TargetHigh (23) → correction = +1
	b.Read(dst)
	// Consumes 2 + 1 = 3
	if b.buffered != 33 {
		t.Fatalf("read 3: expected buffered=33 (correction applied), got %d", b.buffered)
	}
	if b.samplesDropped != 1 {
		t.Fatalf("expected samplesDropped=1, got %d", b.samplesDropped)
	}
}

func TestStereoWriteRead(t *testing.T) {
	cfg := testConfig()
	cfg.NumChannels = 2
	cfg.CorrectionInterval = 1000 // disable correction
	b := NewCircularBuffer(cfg)
	// TargetLow = 17 samples. Stereo: need 17*2 = 34 floats to transition.

	// Write stereo data: L=positive, R=negative
	data := make([]float32, 40)
	for i := 0; i < 40; i += 2 {
		data[i] = float32(i/2 + 1)
		data[i+1] = float32(-(i/2 + 1))
	}
	b.Write(data) // 20 stereo samples = 40 floats

	dst := make([]float32, 8) // 4 stereo samples
	ok := b.Read(dst)
	if !ok {
		t.Fatal("expected Read to return true")
	}

	// Verify channel alignment
	for i := 0; i < 8; i += 2 {
		if dst[i] <= 0 {
			t.Fatalf("expected positive L at dst[%d], got %f", i, dst[i])
		}
		if dst[i+1] >= 0 {
			t.Fatalf("expected negative R at dst[%d], got %f", i+1, dst[i+1])
		}
	}
	if b.buffered != 32 { // 40 - 8
		t.Fatalf("expected buffered=32, got %d", b.buffered)
	}
}

func TestStereoDriftCorrectionDrop(t *testing.T) {
	cfg := testConfig()
	cfg.NumChannels = 2
	cfg.CorrectionInterval = 1
	b := NewCircularBuffer(cfg)
	// TargetHigh = 23 samples. Write 24 stereo samples = 48 floats → above target → correction = +1

	data := make([]float32, 48)
	for i := 0; i < 48; i += 2 {
		data[i] = float32(i/2 + 1)      // L: 1, 2, 3, ...
		data[i+1] = float32(-(i/2 + 1)) // R: -1, -2, -3, ...
	}
	b.Write(data)

	dst := make([]float32, 8) // 4 stereo samples
	b.Read(dst)
	// correction = +1 sample pair = 2 floats. Consumes 8 + 2 = 10 floats.
	if b.buffered != 38 { // 48 - 10
		t.Fatalf("expected buffered=38, got %d", b.buffered)
	}

	// Verify stereo alignment preserved
	for i := 0; i < 8; i += 2 {
		if dst[i] <= 0 {
			t.Fatalf("expected positive L at dst[%d], got %f", i, dst[i])
		}
		if dst[i+1] >= 0 {
			t.Fatalf("expected negative R at dst[%d], got %f", i+1, dst[i+1])
		}
	}
}

func TestStereoDriftCorrectionInsert(t *testing.T) {
	cfg := testConfig()
	cfg.NumChannels = 2
	cfg.CorrectionInterval = 1
	cfg.MinLatencyMs = 1
	b := NewCircularBuffer(cfg)
	// TargetLow=17 samples. Stereo: need 17*2=34 floats to transition.

	// Write 20 stereo samples = 40 floats (20 samples >= TargetLow=17)
	data := make([]float32, 40)
	for i := 0; i < 40; i += 2 {
		data[i] = float32(i/2 + 1)      // L
		data[i+1] = float32(-(i/2 + 1)) // R
	}
	b.Write(data)

	// First read: transitions to PLAYING, fillSamples=20 in target zone, no correction
	dst := make([]float32, 8) // 4 stereo samples
	b.Read(dst) // buffered = 32, fillSamples = 16, below TargetLow=17

	// Second read: fillSamples=16 < TargetLow=17 → correction = -1 sample pair
	dst2 := make([]float32, 8)
	b.Read(dst2)
	// correction = -1, realFloats = 8 + (-1)*2 = 6. Reads 6 floats (3 pairs), duplicates last pair.
	if b.buffered != 26 { // 32 - 6 = 26
		t.Fatalf("expected buffered=26, got %d", b.buffered)
	}

	// Verify all pairs have correct channel alignment
	for i := 0; i < 8; i += 2 {
		if dst2[i] <= 0 {
			t.Fatalf("expected positive L at dst2[%d], got %f", i, dst2[i])
		}
		if dst2[i+1] >= 0 {
			t.Fatalf("expected negative R at dst2[%d], got %f", i+1, dst2[i+1])
		}
	}
	// Last stereo pair should be duplicated from second-to-last
	if dst2[6] != dst2[4] || dst2[7] != dst2[5] {
		t.Fatalf("expected duplicated last stereo pair: got [%f,%f] vs [%f,%f]",
			dst2[6], dst2[7], dst2[4], dst2[5])
	}
}

func TestGetStats(t *testing.T) {
	cfg := testConfig()
	cfg.SampleRate = 1000
	b := NewCircularBuffer(cfg)

	stats := b.GetStats()
	if stats.State != "filling" {
		t.Fatalf("expected state=filling, got %s", stats.State)
	}
	if stats.FillLevelSamples != 0 {
		t.Fatalf("expected fill=0, got %d", stats.FillLevelSamples)
	}

	b.Write(make([]float32, 10))
	stats = b.GetStats()
	if stats.FillLevelSamples != 10 {
		t.Fatalf("expected fill=10, got %d", stats.FillLevelSamples)
	}
	if stats.FillLevelMs != 10.0 {
		t.Fatalf("expected fillMs=10.0, got %f", stats.FillLevelMs)
	}

	// Transition to playing
	b.Write(make([]float32, 10)) // total 20
	dst := make([]float32, 2)
	b.Read(dst) // playing, buffered = 18

	stats = b.GetStats()
	if stats.State != "playing" {
		t.Fatalf("expected state=playing, got %s", stats.State)
	}
	if stats.FillLevelSamples != 18 {
		t.Fatalf("expected fill=18, got %d", stats.FillLevelSamples)
	}
}

func TestGetStatsZone(t *testing.T) {
	cfg := testConfig()
	cfg.CorrectionInterval = 1000
	b := NewCircularBuffer(cfg)
	// TargetLow=17, TargetHigh=23

	// Fill to above target (24 samples, > TargetHigh=23)
	b.Write(make([]float32, 24))
	stats := b.GetStats()
	if stats.CurrentZone != 1 {
		t.Fatalf("expected zone=1 (above target), got %d", stats.CurrentZone)
	}

	// Fill to within target (20 samples)
	b2 := NewCircularBuffer(cfg)
	b2.Write(make([]float32, 20))
	stats = b2.GetStats()
	if stats.CurrentZone != 0 {
		t.Fatalf("expected zone=0 (in target), got %d", stats.CurrentZone)
	}
}

func TestGetBehindBackwardCompat(t *testing.T) {
	b := NewCircularBuffer(testConfig())
	b.Write(make([]float32, 15))
	if b.GetBehind() != 15 {
		t.Fatalf("expected GetBehind=15, got %d", b.GetBehind())
	}
}

func TestFillingDoesNotConsumeData(t *testing.T) {
	b := NewCircularBuffer(testConfig())
	// Write less than TargetLow
	b.Write(make([]float32, 5))
	dst := make([]float32, 4)
	b.Read(dst) // FILLING, outputs silence

	// Buffered should be unchanged (read didn't consume)
	if b.buffered != 5 {
		t.Fatalf("expected buffered=5 after filling read, got %d", b.buffered)
	}
}

func TestRefillingAfterUnderrun(t *testing.T) {
	cfg := testConfig()
	cfg.MinLatencyMs = 5
	b := NewCircularBuffer(cfg)
	// TargetLow = 17, Min = 5

	// Get to playing
	b.Write(make([]float32, 20))
	dst := make([]float32, 16)
	b.Read(dst) // playing, buffered = 4

	// Trigger underrun
	b.Read(dst) // fillSamples = 4 < min = 5 → FILLING
	if b.state != stateFilling {
		t.Fatal("should be filling after underrun")
	}

	// Write some but not enough for TargetLow
	b.Write(make([]float32, 10))
	ok := b.Read(dst) // still filling (14 < 17)
	if ok {
		t.Fatal("should still be filling")
	}

	// Write enough to reach TargetLow
	b.Write(make([]float32, 5)) // total = 4 + 10 + 5 = 19
	ok = b.Read(dst)            // 19 >= 17 → transition to playing
	if !ok {
		t.Fatal("should be playing again")
	}
	if b.state != statePlaying {
		t.Fatalf("expected state=playing, got %s", b.state)
	}
}
