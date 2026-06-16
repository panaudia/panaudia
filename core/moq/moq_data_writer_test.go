package moq

import (
	"bytes"
	"strings"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/statecache"
	"github.com/pion/webrtc/v3/pkg/media"
)

// captureSink implements entitySink and records every WriteSample
// payload it receives, so tests can assert on the bytes that would
// reach the client's entity output track.
type captureSink struct {
	writes [][]byte
	err    error
}

func (c *captureSink) WriteSample(sample media.Sample) error {
	cp := make([]byte, len(sample.Data))
	copy(cp, sample.Data)
	c.writes = append(c.writes, cp)
	return c.err
}

// newWriterWithSink builds a MoqDataWriter wired to a capturing entity
// sink, suitable for testing the sendEntity filter+rebuild path without
// needing a real moqtransport session.
func newWriterWithSink(t *testing.T, myID uuid.UUID, sink entitySink) *MoqDataWriter {
	t.Helper()
	return &MoqDataWriter{
		entityAdapter: sink,
		myNodeID:      myID,
		myNodeIDStr:   myID.String(),
		subSpaces:     mapset.NewSet[uuid.UUID](),
		members:       mapset.NewSet[uuid.UUID](),
		nodeSubspaces: make(map[uuid.UUID]mapset.Set[uuid.UUID]),
	}
}

// encodeBatch builds a cache envelope wrapping a JSON batch of the given ops.
func encodeBatch(t *testing.T, topic, key string, opID uint64, ops [][]byte) []byte {
	t.Helper()
	var inner []byte
	if len(ops) == 1 {
		inner = ops[0]
	} else {
		var err error
		inner, err = statecache.BuildBatch(ops)
		if err != nil {
			t.Fatalf("BuildBatch: %v", err)
		}
	}
	envelope, err := statecache.Encode(statecache.Op{
		Topic: topic,
		Key:   key,
		Value: inner,
		OpID:  opID,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return envelope
}

func TestEntityKeyVisible(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	w := &MoqDataWriter{myNodeID: myID, myNodeIDStr: myID.String()}

	cases := []struct {
		key  string
		want bool
	}{
		{myID.String(), true},                                // existence marker for self
		{myID.String() + ".subspaces.abcd", true},            // self subfield
		{myID.String() + ".mutes." + otherID.String(), true}, // self nested path
		{otherID.String(), false},                            // other's marker
		{otherID.String() + ".muted", false},                 // other's subfield
		{"", false},                                          // empty
		{myID.String() + "extra", false},                     // accidental prefix without dot must NOT match
		{strings.Repeat("a", 36), false},                     // same length, different content
	}
	for _, c := range cases {
		got := w.entityKeyVisible(c.key)
		if got != c.want {
			t.Errorf("entityKeyVisible(%q): got %v, want %v", c.key, got, c.want)
		}
	}
}

// TestSendEntityDropsAllForeignBatch builds a batch envelope of entity ops
// for a foreign uuid, then verifies sendEntity emits nothing (no adapter
// write) and does not panic when entityAdapter is nil.
func TestSendEntityDropsAllForeignBatch(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	w := &MoqDataWriter{myNodeID: myID, myNodeIDStr: myID.String()}

	op1, err := statecache.BuildOp(otherID.String(), true)
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	op2, err := statecache.BuildOp(otherID.String()+".subspaces.abcd", true)
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	batch, err := statecache.BuildBatch([][]byte{op1, op2})
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	envelope, err := statecache.Encode(statecache.Op{
		Topic: "entity",
		Key:   otherID.String(),
		Value: batch,
		OpID:  42,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// entityAdapter is nil; sendEntity must early-return without panic.
	w.sendEntity(envelope)
}

// TestSendEntityForeignBatchNotForwarded uses a capturing sink to confirm
// that an envelope made entirely of foreign-uuid ops produces zero
// adapter writes — the filter returns early once nothing survives.
func TestSendEntityForeignBatchNotForwarded(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sink := &captureSink{}
	w := newWriterWithSink(t, myID, sink)

	op1, _ := statecache.BuildOp(otherID.String(), true)
	op2, _ := statecache.BuildOp(otherID.String()+".subspaces.abcd", true)
	envelope := encodeBatch(t, "entity", otherID.String(), 42, [][]byte{op1, op2})

	w.sendEntity(envelope)

	if len(sink.writes) != 0 {
		t.Fatalf("expected 0 writes, got %d", len(sink.writes))
	}
}

// TestSendEntityMixedBatchKeepsOnlySelf builds a batch envelope holding
// one self op and one foreign op, then asserts the rebuilt envelope on
// the wire contains only the self op (and parses back as a single op,
// not a one-element batch).
func TestSendEntityMixedBatchKeepsOnlySelf(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sink := &captureSink{}
	w := newWriterWithSink(t, myID, sink)

	mySubID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	selfOp, _ := statecache.BuildOp(myID.String()+".subspaces."+mySubID.String(), true)
	foreignOp, _ := statecache.BuildOp(otherID.String()+".subspaces.abcd", true)
	envelope := encodeBatch(t, "entity", myID.String(), 7, [][]byte{selfOp, foreignOp})

	w.sendEntity(envelope)

	if len(sink.writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(sink.writes))
	}

	rebuilt, err := statecache.Decode(sink.writes[0])
	if err != nil {
		t.Fatalf("Decode rebuilt envelope: %v", err)
	}
	if rebuilt.OpID != 7 {
		t.Errorf("OpID preserved: got %d, want 7", rebuilt.OpID)
	}
	if rebuilt.Topic != "entity" {
		t.Errorf("Topic preserved: got %q, want %q", rebuilt.Topic, "entity")
	}
	ops, _, err := statecache.ParseOps(rebuilt.Value)
	if err != nil {
		t.Fatalf("ParseOps: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 inner op, got %d", len(ops))
	}
	wantKey := myID.String() + ".subspaces." + mySubID.String()
	if ops[0].Key != wantKey {
		t.Errorf("kept op key: got %q, want %q", ops[0].Key, wantKey)
	}
}

// TestSendEntityMarkerTombstonePassesThrough verifies that a tombstone
// envelope for the client's own existence marker is forwarded intact —
// the client needs this signal to know its record was removed.
func TestSendEntityMarkerTombstonePassesThrough(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	sink := &captureSink{}
	w := newWriterWithSink(t, myID, sink)

	tomb, err := statecache.BuildTombstoneOp(myID.String())
	if err != nil {
		t.Fatalf("BuildTombstoneOp: %v", err)
	}
	envelope := encodeBatch(t, "entity", myID.String(), 99, [][]byte{tomb})

	w.sendEntity(envelope)

	if len(sink.writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(sink.writes))
	}
	rebuilt, err := statecache.Decode(sink.writes[0])
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	ops, _, err := statecache.ParseOps(rebuilt.Value)
	if err != nil {
		t.Fatalf("ParseOps: %v", err)
	}
	if len(ops) != 1 || !ops[0].Tombstone || ops[0].Key != myID.String() {
		t.Errorf("expected single tombstone for self, got %+v", ops)
	}
}

// TestSendEntityCollisionPrefixRejected uses a uuid that shares the first
// three bytes with `myID` to confirm the dot-suffix check in
// entityKeyVisible — the foreign key is longer than `myID` but does not
// start with `myID + "."`, so it must be rejected.
func TestSendEntityCollisionPrefixRejected(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	// First three bytes match myID's printed form, but it's a different uuid.
	collisionID := uuid.MustParse("11111111-1111-1111-1111-111111111122")
	if !strings.HasPrefix(collisionID.String(), "11111111-1111") {
		t.Fatalf("test setup: expected prefix collision")
	}
	sink := &captureSink{}
	w := newWriterWithSink(t, myID, sink)

	op, _ := statecache.BuildOp(collisionID.String()+".subspaces.abcd", true)
	envelope := encodeBatch(t, "entity", collisionID.String(), 1, [][]byte{op})

	w.sendEntity(envelope)

	if len(sink.writes) != 0 {
		t.Fatalf("collision-prefix uuid leaked through filter (%d writes)", len(sink.writes))
	}
}

// TestHandleEntityMarkerLifecycle drives handleEntity through a full
// node lifecycle (existence marker arrives → subspace key arrives →
// subspace tombstone → marker tombstone) and asserts the writer's
// internal members + nodeSubspaces map mirror each step.
func TestHandleEntityMarkerLifecycle(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	subA := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	w := newWriterWithSink(t, myID, &captureSink{})

	// Step 1: existence marker arrives.
	markerOp, _ := statecache.BuildOp(otherID.String(), true)
	w.handleEntity(encodeBatch(t, "entity", otherID.String(), 1, [][]byte{markerOp}))
	if !w.members.Contains(otherID) {
		t.Errorf("after marker: members should contain otherID")
	}

	// Step 2: subspace key arrives → nodeSubspaces[other] now holds subA.
	ssOp, _ := statecache.BuildOp(otherID.String()+".subspaces."+subA.String(), true)
	w.handleEntity(encodeBatch(t, "entity", otherID.String(), 2, [][]byte{ssOp}))
	if set, ok := w.nodeSubspaces[otherID]; !ok || !set.Contains(subA) {
		t.Errorf("after subspace add: nodeSubspaces[other] should contain subA, got %v", w.nodeSubspaces[otherID])
	}

	// Step 3: subspace tombstone → set drains but member stays alive.
	ssTomb, _ := statecache.BuildTombstoneOp(otherID.String() + ".subspaces." + subA.String())
	w.handleEntity(encodeBatch(t, "entity", otherID.String(), 3, [][]byte{ssTomb}))
	if set, ok := w.nodeSubspaces[otherID]; !ok || set.Contains(subA) {
		t.Errorf("after subspace tombstone: subA should be removed, got %v", w.nodeSubspaces[otherID])
	}
	if !w.members.Contains(otherID) {
		t.Errorf("after subspace tombstone: members should still contain otherID")
	}

	// Step 4: marker tombstone → node fully removed.
	markerTomb, _ := statecache.BuildTombstoneOp(otherID.String())
	w.handleEntity(encodeBatch(t, "entity", otherID.String(), 4, [][]byte{markerTomb}))
	if w.members.Contains(otherID) {
		t.Errorf("after marker tombstone: members should not contain otherID")
	}
	if _, ok := w.nodeSubspaces[otherID]; ok {
		t.Errorf("after marker tombstone: nodeSubspaces[other] should be deleted")
	}
}

// TestSendSpaceWithoutCapDrops locks down the sendSpace gate: an
// envelope reaching SendString("space", ...) when the holder lacks
// commands.ReadCapSpaceRead must not reach the adapter at all. With
// the cap, the same envelope is forwarded verbatim (no per-key
// filter — space keys are uuid-less).
func TestSendSpaceWithoutCapDrops(t *testing.T) {
	myID := uuid.New()
	sink := &captureSink{}

	w := &MoqDataWriter{
		spaceAdapter: sink,
		myNodeID:     myID,
		myNodeIDStr:  myID.String(),
		subSpaces:    mapset.NewSet[uuid.UUID](),
		members:      mapset.NewSet[uuid.UUID](),
		spaceRead:    false, // cap not granted
	}

	op, _ := statecache.BuildOp("roles-muted.performer", true)
	envelope := encodeBatch(t, "space", "roles-muted.performer", 1, [][]byte{op})

	w.SendString("space", string(envelope))

	if got := len(sink.writes); got != 0 {
		t.Errorf("without space.read cap, expected 0 writes; got %d", got)
	}
}

func TestSendSpaceWithCapForwards(t *testing.T) {
	myID := uuid.New()
	sink := &captureSink{}

	w := &MoqDataWriter{
		spaceAdapter: sink,
		myNodeID:     myID,
		myNodeIDStr:  myID.String(),
		subSpaces:    mapset.NewSet[uuid.UUID](),
		members:      mapset.NewSet[uuid.UUID](),
		spaceRead:    true, // cap granted
	}

	op, _ := statecache.BuildOp("roles-muted.performer", true)
	envelope := encodeBatch(t, "space", "roles-muted.performer", 1, [][]byte{op})

	w.SendString("space", string(envelope))

	if got := len(sink.writes); got != 1 {
		t.Fatalf("with space.read cap, expected 1 write; got %d", got)
	}
	// Forwarded verbatim — no envelope rebuild for space topic.
	if !bytes.Equal(sink.writes[0], envelope) {
		t.Errorf("space envelope should be forwarded byte-for-byte; got %d bytes vs %d expected", len(sink.writes[0]), len(envelope))
	}
}
