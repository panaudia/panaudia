package direct

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/statecache"
)

// testSender captures messages sent via the bouncer's receive path.
type testSender struct {
	mu         sync.Mutex
	stringMsgs []StringMessage
	dataMsgs   []DataMessage
}

func (s *testSender) SendString(topic string, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stringMsgs = append(s.stringMsgs, StringMessage{topic: topic, msg: msg})
}

func (s *testSender) SendData(topic string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dataMsgs = append(s.dataMsgs, DataMessage{topic: topic, msg: data})
}

func (s *testSender) getStringMsgs() []StringMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StringMessage, len(s.stringMsgs))
	copy(out, s.stringMsgs)
	return out
}

func (s *testSender) getDataMsgs() []DataMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DataMessage, len(s.dataMsgs))
	copy(out, s.dataMsgs)
	return out
}

// waitForStringMsgs polls until at least n messages arrive or timeout.
func (s *testSender) waitForStringMsgs(n int, timeout time.Duration) []StringMessage {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs := s.getStringMsgs()
		if len(msgs) >= n {
			return msgs
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.getStringMsgs()
}

// newTestBackend creates a minimal DirectBackend for cache testing.
func newTestBackend() *DirectBackend {
	backend := &DirectBackend{
		ChannelCount:      2,
		HandlersByUuid:    make(map[uuid.UUID]*ConnectionHandler),
		BouncersByUuid:    make(map[uuid.UUID]*Bouncer),
		ConnKeysBySubject: make(map[uuid.UUID]map[string]map[string]bool),
		StringChIn:        make(chan StringMessage, 1000),
		DataChIn:          make(chan DataMessage, 1000),
		Quit:              make(chan int),
		Cache:             statecache.New(statecache.DefaultConfig()),
		CachePolicy:       statecache.DefaultPolicy(),
		KeyExtractor:      statecache.DefaultKeyExtractor(),
		Sessions:          sessions.NewRegistry(),
		pendingFree:       make(map[uuid.UUID][]*ConnectionHandler),
	}

	go func() {
		for {
			select {
			case msg := <-backend.StringChIn:
				backend.handleStringMessage(msg)
			case msg := <-backend.DataChIn:
				backend.handleDataMessage(msg)
			case <-backend.Quit:
				return
			}
		}
	}()

	return backend
}

// addTestBouncer creates a bouncer with a testSender and registers it.
func addTestBouncer(backend *DirectBackend, nodeID uuid.UUID) (*Bouncer, *testSender) {
	bouncer := NewBouncer(nodeID, backend.StringChIn, backend.DataChIn)
	sender := &testSender{}
	bouncer.SetReceiveSender(sender)

	backend.Lock()
	backend.BouncersByUuid[nodeID] = bouncer
	backend.Unlock()

	return bouncer, sender
}

// --- helpers to build op-format messages ---

func buildSingleOp(key, value string) string {
	op, _ := statecache.BuildOp(key, value)
	return string(op)
}

func buildBatchOps(kvs [][2]string) string {
	ops := make([][]byte, len(kvs))
	for i, kv := range kvs {
		ops[i], _ = statecache.BuildOp(kv[0], kv[1])
	}
	batch, _ := statecache.BuildBatch(ops)
	return string(batch)
}

// --- Write path tests ---

func TestStringMessageCached(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	nodeID := uuid.UUID{1}
	bouncer, sender := addTestBouncer(backend, nodeID)

	// Send a single attributes op
	msg := buildSingleOp("node-A.name", "alice")
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg, sourceUUID: nodeID}

	msgs := sender.waitForStringMsgs(1, 500*time.Millisecond)
	if len(msgs) == 0 {
		t.Fatal("bouncer received no messages")
	}

	// Verify the broadcast message is wrapped in a cache envelope
	if !statecache.IsCacheEnvelope([]byte(msgs[0].msg)) {
		t.Error("broadcast message should be wrapped in cache envelope")
	}

	// Verify the op is in the store
	ops := backend.Cache.Snapshot()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op in store, got %d", len(ops))
	}
	if ops[0].Key != "node-A.name" {
		t.Errorf("expected key %q, got %q", "node-A.name", ops[0].Key)
	}

	bouncer.Stop()
}

func TestBatchMessageCached(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	nodeID := uuid.UUID{1}
	bouncer, sender := addTestBouncer(backend, nodeID)

	// Send a batch of 3 ops
	msg := buildBatchOps([][2]string{
		{"node-A.name", "alice"},
		{"node-A.ticket.colour", "#ff6633"},
		{"node-A.connection", "webrtc"},
	})
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg, sourceUUID: nodeID}

	msgs := sender.waitForStringMsgs(1, 500*time.Millisecond)
	if len(msgs) == 0 {
		t.Fatal("bouncer received no messages")
	}

	// Single broadcast message (batch wrapped in one envelope)
	if !statecache.IsCacheEnvelope([]byte(msgs[0].msg)) {
		t.Error("broadcast message should be wrapped in cache envelope")
	}

	// All 3 keys should be in the store individually
	ops := backend.Cache.Snapshot()
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops in store, got %d", len(ops))
	}

	keys := make(map[string]bool)
	for _, op := range ops {
		keys[op.Key] = true
	}
	for _, expected := range []string{"node-A.name", "node-A.ticket.colour", "node-A.connection"} {
		if !keys[expected] {
			t.Errorf("expected key %q in store", expected)
		}
	}

	bouncer.Stop()
}

func TestConnKeysBySubjectTracked(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	nodeID := uuid.UUID{1}
	_, sender := addTestBouncer(backend, nodeID)

	nameKey := nodeID.String() + ".name"
	ticketKey := nodeID.String() + ".ticket.colour"
	msg := buildBatchOps([][2]string{
		{nameKey, "alice"},
		{ticketKey, "#ff6633"},
	})
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg, sourceUUID: nodeID}

	// The broadcast happens after key tracking, so once the bouncer has
	// seen the envelope the keys are in place; the lock gives the read
	// a happens-before edge with the dispatcher's write.
	sender.waitForStringMsgs(1, 500*time.Millisecond)

	backend.Lock()
	keys := backend.ConnKeysBySubject[nodeID]["attributes"]
	if len(keys) != 2 {
		backend.Unlock()
		t.Fatalf("expected 2 keys tracked for subject, got %d", len(keys))
	}
	ok := keys[nameKey] && keys[ticketKey]
	backend.Unlock()
	if !ok {
		t.Errorf("unexpected keys: %v", keys)
	}
}

func TestDataMessageNotCachedByDefault(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	nodeID := uuid.UUID{1}
	bouncer, sender := addTestBouncer(backend, nodeID)

	// Send a "state" message (not cached by default policy)
	backend.DataChIn <- DataMessage{topic: "state", msg: []byte("some state data")}

	time.Sleep(100 * time.Millisecond)
	dataMsgs := sender.getDataMsgs()
	if len(dataMsgs) == 0 {
		t.Fatal("bouncer should receive state message even though not cached")
	}

	// The message should NOT be wrapped
	if statecache.IsCacheEnvelope(dataMsgs[0].msg) {
		t.Error("state message should not be wrapped in cache envelope")
	}

	// Store should be empty
	ops := backend.Cache.Snapshot()
	if len(ops) != 0 {
		t.Errorf("expected 0 ops in store, got %d", len(ops))
	}

	bouncer.Stop()
}

func TestOpIDsMonotonicallyIncreasing(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	nodeID := uuid.UUID{1}
	bouncer, sender := addTestBouncer(backend, nodeID)

	// Send three separate single ops
	for _, name := range []string{"alice", "bob", "charlie"} {
		msg := buildSingleOp("node-"+name+".name", name)
		backend.StringChIn <- StringMessage{topic: "attributes", msg: msg, sourceUUID: nodeID}
	}

	msgs := sender.waitForStringMsgs(3, 500*time.Millisecond)
	if len(msgs) < 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	var lastOpID uint64
	for i, m := range msgs {
		op, err := statecache.Decode([]byte(m.msg))
		if err != nil {
			t.Fatalf("failed to decode message %d: %v", i, err)
		}
		if op.OpID <= lastOpID && i > 0 {
			t.Errorf("opID %d not greater than previous %d", op.OpID, lastOpID)
		}
		lastOpID = op.OpID
	}

	bouncer.Stop()
}

func TestCachingDoesNotEatMessage(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	_, sender1 := addTestBouncer(backend, uuid.UUID{1})
	_, sender2 := addTestBouncer(backend, uuid.UUID{2})

	msg := buildSingleOp("test-uuid.name", "test")
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg}

	msgs1 := sender1.waitForStringMsgs(1, 500*time.Millisecond)
	msgs2 := sender2.waitForStringMsgs(1, 500*time.Millisecond)

	if len(msgs1) != 1 {
		t.Errorf("bouncer1 expected 1 message, got %d", len(msgs1))
	}
	if len(msgs2) != 1 {
		t.Errorf("bouncer2 expected 1 message, got %d", len(msgs2))
	}
}

// --- Backfill tests ---

func TestBackfillSendsAllEntries(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	// Populate the store directly with individual ops
	for i, name := range []string{"alice", "bob", "charlie"} {
		opID := backend.OpIDCounter.Assign()
		key := "node-" + name + ".name"
		value, _ := statecache.BuildOp(key, name)
		backend.Cache.Set("attributes", key, value, opID, uint32(i))
	}

	nodeID := uuid.UUID{10}
	_, sender := addTestBouncer(backend, nodeID)

	backend.BackfillBouncer(nodeID, 0)

	msgs := sender.waitForStringMsgs(3, 500*time.Millisecond)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 backfill messages, got %d", len(msgs))
	}

	for i, m := range msgs {
		if !statecache.IsCacheEnvelope([]byte(m.msg)) {
			t.Errorf("backfill message %d not a cache envelope", i)
		}
	}
}

func TestBackfillWithResumeOpID(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	var secondOpID uint64
	for _, name := range []string{"alice", "bob", "charlie"} {
		opID := backend.OpIDCounter.Assign()
		if name == "bob" {
			secondOpID = opID
		}
		key := "node-" + name + ".name"
		value, _ := statecache.BuildOp(key, name)
		backend.Cache.Set("attributes", key, value, opID, 0)
	}

	nodeID := uuid.UUID{10}
	_, sender := addTestBouncer(backend, nodeID)

	backend.BackfillBouncer(nodeID, secondOpID)

	msgs := sender.waitForStringMsgs(1, 500*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 backfill message (only charlie), got %d", len(msgs))
	}

	op, err := statecache.Decode([]byte(msgs[0].msg))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if op.Key != "node-charlie.name" {
		t.Errorf("expected key node-charlie.name, got %s", op.Key)
	}
}

func TestBackfillEmptyStore(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	nodeID := uuid.UUID{10}
	_, sender := addTestBouncer(backend, nodeID)

	backend.BackfillBouncer(nodeID, 0)

	time.Sleep(100 * time.Millisecond)
	msgs := sender.getStringMsgs()
	if len(msgs) != 0 {
		t.Errorf("expected 0 backfill messages from empty store, got %d", len(msgs))
	}
}

func TestBackfillWhileLiveMessages(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	// Populate store
	for _, name := range []string{"existing1", "existing2"} {
		opID := backend.OpIDCounter.Assign()
		key := name + ".name"
		value, _ := statecache.BuildOp(key, name)
		backend.Cache.Set("attributes", key, value, opID, 0)
	}

	nodeID := uuid.UUID{10}
	_, sender := addTestBouncer(backend, nodeID)

	backend.BackfillBouncer(nodeID, 0)

	// Send a live message
	msg := buildSingleOp("live-node.name", "live")
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg}

	msgs := sender.waitForStringMsgs(3, 500*time.Millisecond)

	if len(msgs) < 3 {
		t.Errorf("expected at least 3 messages (2 backfill + 1 live), got %d", len(msgs))
	}
}

// TestBackfillTopicOrder asserts BackfillBouncer's bucket ordering:
// entity envelopes are emitted before attributes, attributes before
// space, and space before any other (currently-undefined) topics.
// The contract is described in
// plan/history/distributed-state-sync/topic-ordering.md and matters because
// the writer's per-source visibility filter relies on entity-first
// arrival to populate nodeSubspaces before attributes flow through
// uuidVisible.
func TestBackfillTopicOrder(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	// Seed the cache with one op per topic so backfill emits exactly
	// three envelopes whose ordering we can assert on.
	mustPut := func(topic, key, value string) {
		opID := backend.OpIDCounter.Assign()
		raw, _ := statecache.BuildOp(key, value)
		backend.Cache.Set(topic, key, raw, opID, 0)
	}
	mustPut("attributes", "n1.name", "alice")
	mustPut("entity", "n1", "true")
	mustPut("space", "roles-muted.performer", "true")

	nodeID := uuid.UUID{10}
	_, sender := addTestBouncer(backend, nodeID)

	backend.BackfillBouncer(nodeID, 0)

	msgs := sender.waitForStringMsgs(3, 500*time.Millisecond)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 backfill messages, got %d", len(msgs))
	}
	got := []string{msgs[0].topic, msgs[1].topic, msgs[2].topic}
	want := []string{"entity", "attributes", "space"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backfill topic order: got %v, want %v", got, want)
			break
		}
	}
}

// --- Tombstone tests ---

func TestTombstoneWrittenToStore(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	// Write an entry first
	opID := backend.OpIDCounter.Assign()
	key := "node-A.name"
	value, _ := statecache.BuildOp(key, "departing")
	backend.Cache.Set("attributes", key, value, opID, 0)

	// Verify entry exists
	ops := backend.Cache.Snapshot()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op before tombstone, got %d", len(ops))
	}

	// Write tombstone
	tombOpID := backend.OpIDCounter.Assign()
	backend.Cache.Tomb("attributes", key, tombOpID, 0)

	backend.Cache.Compact()
	ops = backend.Cache.Snapshot()

	found := false
	for _, op := range ops {
		if op.Key == key {
			if !op.Tombstone {
				t.Error("expected tombstone for departed node")
			}
			found = true
		}
	}
	if !found {
		t.Error("tombstone should still be present within TTL")
	}
}

func TestBackfillSkipsTombstonedEntries(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	// Write an entry then tombstone it
	opID1 := backend.OpIDCounter.Assign()
	key1 := "departed.name"
	value1, _ := statecache.BuildOp(key1, "gone")
	backend.Cache.Set("attributes", key1, value1, opID1, 0)

	opID2 := backend.OpIDCounter.Assign()
	backend.Cache.Tomb("attributes", key1, opID2, 0)

	// Write a live entry
	opID3 := backend.OpIDCounter.Assign()
	key2 := "alive.name"
	value2, _ := statecache.BuildOp(key2, "present")
	backend.Cache.Set("attributes", key2, value2, opID3, 0)

	nodeID := uuid.UUID{10}
	_, sender := addTestBouncer(backend, nodeID)

	backend.BackfillBouncer(nodeID, 0)

	msgs := sender.waitForStringMsgs(1, 500*time.Millisecond)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 backfill message (only alive), got %d", len(msgs))
	}

	op, err := statecache.Decode([]byte(msgs[0].msg))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if op.Key != key2 {
		t.Errorf("expected key %q, got %s", key2, op.Key)
	}
}

// --- FreeSource backstop test ---

// TestFreeSourceBackstopDeparture: FreeSource (the mixer-tick path,
// today reached by the stale-node timeout) is the funnel backstop — a
// node with a live registry entry that lands there gets the full
// departure sweep via DepartNode.
func TestFreeSourceBackstopDeparture(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.ISpace = newFakeSpace()

	nodeID := uuid.UUID{0xAA}
	_, sender := addTestBouncer(backend, nodeID)
	_, entry := backend.Sessions.Register(nodeID, &sessions.FuncSession{}, "test")

	// Send a batch so keys get tracked
	msg := buildBatchOps([][2]string{
		{nodeID.String() + ".name", "alice"},
		{nodeID.String() + ".ticket.colour", "#ff6633"},
	})
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg, sourceUUID: nodeID}

	// Wait for processing
	sender.waitForStringMsgs(1, 500*time.Millisecond)

	// Verify keys tracked (locked: the dispatcher goroutine writes this map)
	backend.Lock()
	tracked := len(backend.ConnKeysBySubject[nodeID]["attributes"])
	backend.Unlock()
	if tracked != 2 {
		t.Fatalf("expected 2 tracked keys, got %d", tracked)
	}

	// Add a second bouncer to observe the tombstone broadcast
	observerID := uuid.UUID{0xBB}
	_, observerSender := addTestBouncer(backend, observerID)

	// Timeout-style removal: FreeSource with the entry still registered.
	backend.FreeSource(nodeID)

	// The entry departed via the backstop.
	select {
	case <-entry.Departed():
	default:
		t.Fatal("FreeSource backstop did not run the departure")
	}

	// Observer should receive the tombstone broadcast (async via the
	// dispatcher).
	msgs := observerSender.waitForStringMsgs(1, time.Second)
	if len(msgs) == 0 {
		t.Fatal("observer should receive tombstone broadcast")
	}
	if !statecache.IsCacheEnvelope([]byte(msgs[0].msg)) {
		t.Error("tombstone broadcast should be a cache envelope")
	}

	// Both keys should be tombstoned in the store.
	if !waitFor(time.Second, func() bool {
		tombstoned := 0
		for _, op := range backend.Cache.Snapshot() {
			if op.Tombstone {
				tombstoned++
			}
		}
		return tombstoned >= 2
	}) {
		t.Error("expected 2 tombstoned ops in store")
	}

	// ConnKeysBySubject should be cleaned up.
	backend.Lock()
	_, exists := backend.ConnKeysBySubject[nodeID]
	backend.Unlock()
	if exists {
		t.Error("ConnKeysBySubject should be cleaned up after the departure")
	}
}

// --- End-to-end tests ---

func TestE2E_BackfillAfterAttributesSent(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	// Connection A sends attributes
	nodeA := uuid.UUID{0xAA}
	_, senderA := addTestBouncer(backend, nodeA)

	msg := buildSingleOp("node-A.name", "alice")
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg, sourceUUID: nodeA}

	senderA.waitForStringMsgs(1, 200*time.Millisecond)

	// Connection B joins afterward
	nodeB := uuid.UUID{0xBB}
	_, senderB := addTestBouncer(backend, nodeB)
	backend.BackfillBouncer(nodeB, 0)

	msgs := senderB.waitForStringMsgs(1, 500*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatalf("connection B expected 1 backfill message, got %d", len(msgs))
	}

	op, err := statecache.Decode([]byte(msgs[0].msg))
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if op.Key != "node-A.name" {
		t.Errorf("expected key node-A.name, got %s", op.Key)
	}
	if op.Topic != "attributes" {
		t.Errorf("expected topic attributes, got %s", op.Topic)
	}
}

func TestE2E_TombstonePreventsBackfill(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	// Connection A sends attributes
	nodeA := uuid.UUID{0xAA}
	_, senderA := addTestBouncer(backend, nodeA)

	msg := buildSingleOp("node-A.name", "alice")
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg, sourceUUID: nodeA}
	senderA.waitForStringMsgs(1, 200*time.Millisecond)

	// Tombstone the key
	tombOpID := backend.OpIDCounter.Assign()
	backend.Cache.Tomb("attributes", "node-A.name", tombOpID, 0)

	// Connection C joins — should NOT receive A's attributes
	nodeC := uuid.UUID{0xCC}
	_, senderC := addTestBouncer(backend, nodeC)
	backend.BackfillBouncer(nodeC, 0)

	time.Sleep(200 * time.Millisecond)
	msgs := senderC.getStringMsgs()
	if len(msgs) != 0 {
		t.Errorf("connection C should receive no backfill after tombstone, got %d messages", len(msgs))
	}
}

// --- Backward compatibility ---

func TestCacheEnvelopeNotValidJSON(t *testing.T) {
	op := statecache.Op{
		Topic: "attributes",
		Key:   "test-key.name",
		Value: []byte(`{"key":"test-key.name","value":"test"}`),
		OpID:  42,
	}
	encoded, err := statecache.Encode(op)
	if err != nil {
		t.Fatal(err)
	}

	if encoded[0] == '{' || encoded[0] == '[' {
		t.Error("cache envelope should not start with a JSON character")
	}
	if encoded[0] != 0xCA {
		t.Errorf("cache envelope should start with 0xCA, got 0x%02X", encoded[0])
	}
}

// --- Nil policy test ---

func TestNilPolicySkipsCaching(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	backend.CachePolicy = nil

	nodeID := uuid.UUID{1}
	_, sender := addTestBouncer(backend, nodeID)

	msg := buildSingleOp("test-uuid.name", "test")
	backend.StringChIn <- StringMessage{topic: "attributes", msg: msg}

	msgs := sender.waitForStringMsgs(1, 500*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	if statecache.IsCacheEnvelope([]byte(msgs[0].msg)) {
		t.Error("message should not be wrapped when policy is nil")
	}

	ops := backend.Cache.Snapshot()
	if len(ops) != 0 {
		t.Errorf("expected 0 ops in store with nil policy, got %d", len(ops))
	}
}
