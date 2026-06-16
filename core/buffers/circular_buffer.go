package buffers

const (
	stateFilling = "filling"
	statePlaying = "playing"
)

type BufferStatsDelegate interface {
	NotifyReadMiss(miss int)
	NotifySessionGone(sessionId uint64)
}

// ICircularBuffer is the common interface for circular buffer implementations.
type ICircularBuffer interface {
	Write(src []float32)
	Read(dst []float32) bool
	GetBehind() int
	GetStats() CircularBufferStats
}

// BufferImpl selects which legacy ICircularBuffer implementation
// NewICircularBuffer returns. JitterBuffer is no longer routed through this
// factory — callers construct it directly via NewJitterBuffer with the
// transport-specific frame sizes.
type BufferImpl string

const (
	// BufferImplCircular is a state-machine implementation (FILLING / PLAYING)
	// in circular_buffer.go with rate-limited ±1 sample drift correction.
	// Legacy; new callers should use NewJitterBuffer.
	BufferImplCircular BufferImpl = "circular"

	// BufferImplCircularA is the older implementation in circular_buffer_a.go
	// with graduated (1/2/4 sample) drift correction. Used by directroc/ for
	// ROC track input. Legacy; new callers should use NewJitterBuffer.
	BufferImplCircularA BufferImpl = "circular_a"
)

// NewICircularBuffer creates an ICircularBuffer of the requested implementation.
// For CircularBufferA, the config's latency settings are mapped to its positional params.
func NewICircularBuffer(impl BufferImpl, config CircularBufferConfig) ICircularBuffer {
	switch impl {
	case BufferImplCircular, "":
		return NewCircularBuffer(config)
	case BufferImplCircularA:
		sr := defaultVal(config.SampleRate, 48000)
		nc := defaultVal(config.NumChannels, 1)
		minMs := defaultVal(config.MinLatencyMs, 10)
		maxMs := defaultVal(config.MaxLatencyMs, 200)
		capMs := defaultVal(config.CapacityMs, 1000)
		msToSamples := func(ms int) int { return ms * sr / 1000 * nc }
		return NewCircularBufferA(msToSamples(capMs), msToSamples(minMs), msToSamples(maxMs), config.StatsDelegate)
	default:
		panic("buffers: unknown BufferImpl: " + string(impl))
	}
}

// CircularBufferConfig holds configuration for the circular buffer.
// Zero-valued fields use sensible defaults.
type CircularBufferConfig struct {
	SampleRate         int // default 48000
	NumChannels        int // 1 for mono, 2 for stereo (default 1)
	TargetLatencyMs    int // centre of target window (default 60)
	TargetWindowMs     int // width of target window (default 20)
	MinLatencyMs       int // below this = underrun (default 10)
	MaxLatencyMs       int // above this = overrun snap (default 200)
	CapacityMs         int // total ring size (default 1000)
	CorrectionInterval int // reads between corrections (default 16)
	StatsDelegate      BufferStatsDelegate
}

// CircularBufferStats holds diagnostic information about the buffer state.
type CircularBufferStats struct {
	FillLevelSamples int
	FillLevelMs      float64
	CurrentZone      int // -1, 0, or +1 (0 = target window)
	UnderrunCount    int
	OverrunCount     int
	SamplesDropped   int
	SamplesInserted  int
	State            string // "filling" or "playing"
}

// CircularBuffer is a ring buffer that decouples network audio arrival from
// fixed-rate audio processing, with a FILLING/PLAYING state machine and
// single-zone drift correction.
type CircularBuffer struct {
	data     []float32
	capacity int // total floats in ring
	writePos int
	readPos  int
	buffered int // floats currently buffered

	numChannels int
	sampleRate  int

	// Zone boundaries in per-channel samples
	targetLow    int
	targetHigh   int
	targetCentre int
	minSamples   int
	maxSamples   int

	// State machine
	state string

	// Correction
	correctionInterval int
	correctionCounter  int

	// Stats
	underrunCount   int
	overrunCount    int
	samplesDropped  int
	samplesInserted int

	statsDelegate BufferStatsDelegate
}

func defaultVal(val, def int) int {
	if val == 0 {
		return def
	}
	return val
}

// NewCircularBuffer creates a new circular buffer with the given configuration.
func NewCircularBuffer(config CircularBufferConfig) *CircularBuffer {
	sr := defaultVal(config.SampleRate, 48000)
	nc := defaultVal(config.NumChannels, 1)
	targetMs := defaultVal(config.TargetLatencyMs, 40)
	windowMs := defaultVal(config.TargetWindowMs, 40)
	minMs := defaultVal(config.MinLatencyMs, 10)
	maxMs := defaultVal(config.MaxLatencyMs, 200)
	capMs := defaultVal(config.CapacityMs, 1000)
	corrInt := defaultVal(config.CorrectionInterval, 2)

	msToSamples := func(ms int) int {
		return ms * sr / 1000
	}

	targetLowMs := targetMs - windowMs/2
	targetHighMs := targetMs + windowMs/2

	b := &CircularBuffer{
		capacity:    msToSamples(capMs) * nc,
		numChannels: nc,
		sampleRate:  sr,

		targetLow:    msToSamples(targetLowMs),
		targetHigh:   msToSamples(targetHighMs),
		targetCentre: msToSamples(targetMs),

		minSamples: msToSamples(minMs),
		maxSamples: msToSamples(maxMs),

		state:              stateFilling,
		correctionInterval: corrInt,

		statsDelegate: config.StatsDelegate,
	}

	b.data = make([]float32, b.capacity)
	return b
}

// Write copies audio data into the ring buffer. Never blocks.
// If the write would overflow capacity, the read head is advanced to discard oldest data.
func (b *CircularBuffer) Write(src []float32) {
	n := len(src)
	if n == 0 {
		return
	}

	// If write is larger than capacity, only keep the tail
	if n > b.capacity {
		src = src[n-b.capacity:]
		n = b.capacity
	}

	// If ring would overflow, advance read head to discard oldest
	if b.buffered+n > b.capacity {
		excess := (b.buffered + n) - b.capacity
		b.readPos = (b.readPos + excess) % b.capacity
		b.buffered -= excess
	}

	// Copy into ring with wrap
	remain := b.capacity - b.writePos
	if n <= remain {
		copy(b.data[b.writePos:b.writePos+n], src)
		b.writePos = (b.writePos + n) % b.capacity
	} else {
		copy(b.data[b.writePos:], src[:remain])
		copy(b.data[:n-remain], src[remain:])
		b.writePos = n - remain
	}
	b.buffered += n
}

// Read reads audio into dst. Returns true if audio was produced, false if silence
// was output (FILLING state or underrun).
func (b *CircularBuffer) Read(dst []float32) bool {
	floatsRequested := len(dst)
	if floatsRequested == 0 {
		return true
	}

	fillSamples := b.buffered / b.numChannels

	// FILLING state
	if b.state == stateFilling {
		if fillSamples >= b.targetLow {
			b.state = statePlaying
			b.correctionCounter = 0
		} else {
			for i := range dst {
				dst[i] = 0
			}
			//if b.statsDelegate != nil {
			//	b.statsDelegate.NotifyReadMiss(1)
			//}
			return false
		}
	}

	// PLAYING state

	// Overrun snap
	if fillSamples > b.maxSamples {
		snapToFloats := b.targetCentre * b.numChannels
		excess := b.buffered - snapToFloats
		b.readPos = (b.readPos + excess) % b.capacity
		b.buffered = snapToFloats
		b.overrunCount++
		fillSamples = b.buffered / b.numChannels
	}

	// Underrun
	if fillSamples < b.minSamples {
		b.state = stateFilling
		for i := range dst {
			dst[i] = 0
		}
		b.underrunCount++

		return false
	}

	// Drift correction: ±1 sample when outside target window
	correction := 0
	if fillSamples < b.targetLow {
		correction = -1
	} else if fillSamples > b.targetHigh {
		correction = +1
	}

	// Apply correction only every N reads
	b.correctionCounter++
	if b.correctionCounter < b.correctionInterval {
		correction = 0
	} else {
		b.correctionCounter = 0
	}

	// Read from ring with correction
	nc := b.numChannels

	if correction > 0 {
		// Dropping samples: output floatsRequested, consume more from ring
		floatsToConsume := floatsRequested + correction*nc
		if floatsToConsume > b.buffered {
			floatsToConsume = b.buffered
		}
		toOutput := floatsRequested
		if toOutput > b.buffered {
			toOutput = b.buffered
		}
		b.copyFromRing(dst[:toOutput], toOutput)
		for i := toOutput; i < floatsRequested; i++ {
			dst[i] = 0
		}
		b.readPos = (b.readPos + floatsToConsume) % b.capacity
		b.buffered -= floatsToConsume
		b.samplesDropped += correction
		if b.statsDelegate != nil {
			b.statsDelegate.NotifyReadMiss(2)
		}

	} else if correction < 0 {
		// Inserting samples: read fewer floats, duplicate last sample pair
		realFloats := floatsRequested + correction*nc
		if realFloats < nc {
			realFloats = nc
		}
		if realFloats > b.buffered {
			realFloats = b.buffered
		}
		b.copyFromRing(dst[:realFloats], realFloats)
		// Duplicate last sample pair to fill remaining output slots
		if realFloats < floatsRequested {
			lastStart := realFloats - nc
			if lastStart < 0 {
				lastStart = 0
			}
			for i := realFloats; i < floatsRequested; i++ {
				dst[i] = dst[lastStart+(i-realFloats)%nc]
			}
		}
		b.readPos = (b.readPos + realFloats) % b.capacity
		b.buffered -= realFloats
		b.samplesInserted += -correction
		if b.statsDelegate != nil {
			b.statsDelegate.NotifyReadMiss(1)
		}
	} else {
		// No correction
		toRead := floatsRequested
		if toRead > b.buffered {
			toRead = b.buffered
		}
		b.copyFromRing(dst[:toRead], toRead)
		for i := toRead; i < floatsRequested; i++ {
			dst[i] = 0
		}
		b.readPos = (b.readPos + toRead) % b.capacity
		b.buffered -= toRead
	}

	return true
}

// copyFromRing copies n floats from the ring starting at readPos into dst.
// Does not advance readPos.
func (b *CircularBuffer) copyFromRing(dst []float32, n int) {
	remain := b.capacity - b.readPos
	if n <= remain {
		copy(dst[:n], b.data[b.readPos:b.readPos+n])
	} else {
		copy(dst[:remain], b.data[b.readPos:])
		copy(dst[remain:n], b.data[:n-remain])
	}
}

// GetBehind returns the current fill level in floats (for backward compatibility).
func (b *CircularBuffer) GetBehind() int {
	return b.buffered
}

// GetStats returns current diagnostic statistics.
func (b *CircularBuffer) GetStats() CircularBufferStats {
	fillSamples := b.buffered / b.numChannels
	fillMs := float64(fillSamples) / float64(b.sampleRate) * 1000.0

	zone := 0
	if fillSamples < b.targetLow {
		zone = -1
	} else if fillSamples > b.targetHigh {
		zone = 1
	}

	return CircularBufferStats{
		FillLevelSamples: fillSamples,
		FillLevelMs:      fillMs,
		CurrentZone:      zone,
		UnderrunCount:    b.underrunCount,
		OverrunCount:     b.overrunCount,
		SamplesDropped:   b.samplesDropped,
		SamplesInserted:  b.samplesInserted,
		State:            b.state,
	}
}
