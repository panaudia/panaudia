package statecache

import (
	"sync"
	"sync/atomic"
	"time"
)

// Config holds the construction parameters for a StateStore.
type Config struct {
	RingSize          int           // number of segments in the ring (default 16)
	SegmentCapacity   int           // number of op slots per segment (default 128)
	CompactInterval   time.Duration // how often to run background compaction (default 100ms)
	TombstoneTTL      time.Duration // how long tombstones are retained in snapshots (default 30s)
	PressureThreshold float64       // soft threshold (0-1) that triggers immediate compaction (default 0.5)
}

// DefaultConfig returns a Config sized for up to 1000 nodes with headroom.
func DefaultConfig() Config {
	return Config{
		RingSize:          16,
		SegmentCapacity:   128,
		CompactInterval:   100 * time.Millisecond,
		TombstoneTTL:      30 * time.Second,
		PressureThreshold: 0.5,
	}
}

// slot holds an atomic pointer to an Op. A nil pointer means the slot is empty.
// The atomic pointer provides the happens-before guarantee between writer and
// reader without any locks.
type slot struct {
	ptr atomic.Pointer[Op]
}

// segment is a fixed-size array of operation slots within the ring.
type segment struct {
	slots  []slot
	cursor atomic.Int64
	epoch  atomic.Uint64
}

// snapshot is an immutable compacted map of the latest value per key.
type snapshot struct {
	entries    map[string]Op // keyed by topic+":"+key
	through    uint64        // covers all segments up to and including this head value
	compacted  bool          // true after at least one compaction has run
	headCursor int64         // cursor of head segment at compaction time; readOps reads new slots beyond this
}

// StateStore is a lock-free cache of key-value state operations backed by a
// pre-allocated ring of segments with periodic compaction into an immutable
// snapshot.
//
// Normal writes (claiming a slot within a segment) are lock-free: one atomic
// increment plus one atomic pointer store. The advanceMu mutex is only held
// during the rare segment transition (once per SegmentCapacity writes).
type StateStore struct {
	ring     []segment
	ringSize uint64
	segCap   int64

	head      atomic.Uint64
	advanceMu sync.Mutex // protects segment advancement only
	snap      atomic.Pointer[snapshot]

	tombTTL           time.Duration
	pressureThreshold float64

	compactSignal chan struct{}
	done          chan struct{}
	wg            sync.WaitGroup
}

// New creates a StateStore with the given configuration and starts the
// background compaction goroutine.
func New(cfg Config) *StateStore {
	if cfg.RingSize <= 0 {
		cfg.RingSize = 16
	}
	if cfg.SegmentCapacity <= 0 {
		cfg.SegmentCapacity = 128
	}
	if cfg.CompactInterval <= 0 {
		cfg.CompactInterval = 100 * time.Millisecond
	}
	if cfg.TombstoneTTL <= 0 {
		cfg.TombstoneTTL = 30 * time.Second
	}
	if cfg.PressureThreshold <= 0 {
		cfg.PressureThreshold = 0.5
	}

	s := &StateStore{
		ring:              make([]segment, cfg.RingSize),
		ringSize:          uint64(cfg.RingSize),
		segCap:            int64(cfg.SegmentCapacity),
		tombTTL:           cfg.TombstoneTTL,
		pressureThreshold: cfg.PressureThreshold,
		compactSignal:     make(chan struct{}, 1),
		done:              make(chan struct{}),
	}

	for i := range s.ring {
		s.ring[i].slots = make([]slot, cfg.SegmentCapacity)
	}

	initSnap := &snapshot{
		entries:   make(map[string]Op),
		through:   0,
		compacted: false,
	}
	s.snap.Store(initSnap)

	s.wg.Add(1)
	go s.compactionLoop(cfg.CompactInterval)

	return s
}

// Close stops the background compaction goroutine and waits for it to finish.
func (s *StateStore) Close() {
	close(s.done)
	s.wg.Wait()
}

// compositeKey builds the map key used in snapshots.
func compositeKey(topic, key string) string {
	return topic + ":" + key
}

// Set writes a key-value operation into the store.
func (s *StateStore) Set(topic, key string, value []byte, opID uint64, nodeID uint32) {
	var valCopy []byte
	if len(value) > 0 {
		valCopy = make([]byte, len(value))
		copy(valCopy, value)
	}

	op := &Op{
		Topic:     topic,
		Key:       key,
		Value:     valCopy,
		OpID:      opID,
		NodeID:    nodeID,
		Tombstone: false,
		CreatedAt: time.Now(),
	}
	s.writeOp(op)
}

// Tomb writes a tombstone for the given key.
func (s *StateStore) Tomb(topic, key string, opID uint64, nodeID uint32) {
	op := &Op{
		Topic:     topic,
		Key:       key,
		Value:     nil,
		OpID:      opID,
		NodeID:    nodeID,
		Tombstone: true,
		CreatedAt: time.Now(),
	}
	s.writeOp(op)
}

func (s *StateStore) writeOp(op *Op) {
	for {
		h := s.head.Load()
		seg := &s.ring[h%s.ringSize]

		idx := seg.cursor.Add(1) - 1
		if idx >= 0 && idx < s.segCap {
			seg.slots[idx].ptr.Store(op)
			s.checkPressure()
			return
		}

		// Segment is full. Take the advance lock — only one goroutine
		// prepares the next segment, others wait briefly then retry.
		s.advanceMu.Lock()
		if s.head.Load() == h {
			// Still needs advancing. Prepare the next segment fully before
			// making it visible by updating head.
			next := h + 1
			nextSeg := &s.ring[next%s.ringSize]
			nextSeg.epoch.Store(next / s.ringSize)
			nextSeg.cursor.Store(0)
			s.head.Store(next)
		}
		s.advanceMu.Unlock()
		// Retry on the new (or already-advanced) head.
	}
}

func (s *StateStore) checkPressure() {
	if s.Pressure() >= s.pressureThreshold {
		select {
		case s.compactSignal <- struct{}{}:
		default:
		}
	}
}

// Snapshot returns all current entries: the compacted snapshot merged with
// any uncompacted segments. Entries are not deduplicated — callers should
// keep the highest OpID per key.
func (s *StateStore) Snapshot() []Op {
	return s.readOps(0)
}

// Since returns only operations with an OpID strictly greater than the given value.
func (s *StateStore) Since(opID uint64) []Op {
	return s.readOps(opID)
}

func (s *StateStore) readOps(minOpID uint64) []Op {
	snap := s.snap.Load()
	h := s.head.Load()

	var ops []Op

	// Snapshot entries.
	for _, op := range snap.entries {
		if minOpID == 0 || op.OpID > minOpID {
			ops = append(ops, op)
		}
	}

	// Uncompacted segments between snapshot boundary and head (inclusive).
	start := snap.through
	if snap.compacted {
		start = snap.through + 1
	}

	// If compaction covered the head segment (through == h), new writes
	// may have arrived to that segment after compaction.  Read only the
	// new slots (from headCursor onward) to avoid resurrecting entries
	// that compaction already merged or purged.
	if snap.compacted && snap.through == h {
		seg := &s.ring[h%s.ringSize]
		if seg.epoch.Load() == h/s.ringSize {
			n := seg.cursor.Load()
			if n > s.segCap {
				n = s.segCap
			}
			for j := snap.headCursor; j < n; j++ {
				op := seg.slots[j].ptr.Load()
				if op == nil {
					continue
				}
				if minOpID == 0 || op.OpID > minOpID {
					ops = append(ops, *op)
				}
			}
		}
	}

	for i := start; i <= h; i++ {
		seg := &s.ring[i%s.ringSize]
		if seg.epoch.Load() != i/s.ringSize {
			continue // stale segment from a previous ring pass
		}
		n := seg.cursor.Load()
		if n > s.segCap {
			n = s.segCap
		}
		for j := int64(0); j < n; j++ {
			op := seg.slots[j].ptr.Load()
			if op == nil {
				continue
			}
			if minOpID == 0 || op.OpID > minOpID {
				ops = append(ops, *op)
			}
		}
	}

	return ops
}

// Pressure returns the fraction of the ring occupied by uncompacted segments,
// from 0.0 (fully compacted) to 1.0 (ring is full).
func (s *StateStore) Pressure() float64 {
	snap := s.snap.Load()
	h := s.head.Load()
	dist := h - snap.through
	return float64(dist) / float64(s.ringSize)
}

// Compact merges the current snapshot with sealed segments to produce a new
// snapshot. This is called by the background goroutine but can also be called
// manually (e.g. in tests).
func (s *StateStore) Compact() {
	snap := s.snap.Load()
	h := s.head.Load()

	if snap.compacted && h == snap.through {
		return // nothing new to compact
	}

	merged := make(map[string]Op, len(snap.entries))
	for k, v := range snap.entries {
		merged[k] = v
	}

	start := snap.through
	if snap.compacted {
		start = snap.through + 1
	}

	for i := start; i <= h; i++ {
		seg := &s.ring[i%s.ringSize]
		if seg.epoch.Load() != i/s.ringSize {
			continue
		}
		n := seg.cursor.Load()
		if n > s.segCap {
			n = s.segCap
		}
		for j := int64(0); j < n; j++ {
			op := seg.slots[j].ptr.Load()
			if op == nil {
				continue
			}
			ck := compositeKey(op.Topic, op.Key)
			if existing, ok := merged[ck]; !ok || op.OpID > existing.OpID {
				merged[ck] = *op
			}
		}
	}

	// Purge expired tombstones using wall-clock CreatedAt.
	if s.tombTTL > 0 {
		cutoff := time.Now().Add(-s.tombTTL)
		for k, op := range merged {
			if op.Tombstone && !op.CreatedAt.IsZero() && op.CreatedAt.Before(cutoff) {
				delete(merged, k)
			}
		}
	}

	// Record how far the head segment's cursor was at this point, so
	// readOps can pick up any new slots written after this compaction.
	headCursor := s.ring[h%s.ringSize].cursor.Load()
	if headCursor > s.segCap {
		headCursor = s.segCap
	}

	newSnap := &snapshot{
		entries:    merged,
		through:    h,
		compacted:  true,
		headCursor: headCursor,
	}
	s.snap.Store(newSnap)
}

func (s *StateStore) compactionLoop(interval time.Duration) {
	defer s.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.Compact()
		case <-s.compactSignal:
			s.Compact()
		}
	}
}
