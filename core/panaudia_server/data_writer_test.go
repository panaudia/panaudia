package panaudia_server

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/statecache"
)

// encodeEntityBatch wraps the given JSON ops in a cache envelope on the
// "entity" topic. Returned bytes can be fed straight to handleEntity.
func encodeEntityBatch(t *testing.T, key string, opID uint64, ops [][]byte) []byte {
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
		Topic: "entity",
		Key:   key,
		Value: inner,
		OpID:  opID,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return envelope
}

func TestDataWriterEntityKeyVisible(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	w := &DataWriter{MyNodeID: myID, myNodeIDStr: myID.String()}

	cases := []struct {
		key  string
		want bool
	}{
		{myID.String(), true},
		{myID.String() + ".subspaces.abcd", true},
		{myID.String() + ".mutes." + otherID.String(), true},
		{otherID.String(), false},
		{otherID.String() + ".muted", false},
		{"", false},
		{myID.String() + "extra", false},
		{strings.Repeat("a", 36), false},
	}
	for _, c := range cases {
		got := w.entityKeyVisible(c.key)
		if got != c.want {
			t.Errorf("entityKeyVisible(%q): got %v, want %v", c.key, got, c.want)
		}
	}
}

// TestDataWriterSendEntityNoChannelNoPanic verifies that sendEntity is a
// no-op when EntityDataChannel is nil, even with a well-formed envelope.
func TestDataWriterSendEntityNoChannelNoPanic(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	w := &DataWriter{MyNodeID: myID, myNodeIDStr: myID.String()}

	op, err := statecache.BuildOp(myID.String(), true)
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	envelope, err := statecache.Encode(statecache.Op{
		Topic: "entity",
		Key:   myID.String(),
		Value: op,
		OpID:  1,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	w.sendEntity(envelope)
}

// TestDataWriterHandleEntityMarkerLifecycle drives the WebRTC writer's
// handleEntity through the same lifecycle the MOQ writer covers, so the
// two transports stay behaviourally aligned.
func TestDataWriterHandleEntityMarkerLifecycle(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	subA := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	w := NewDataWriter(myID, nil, nil)

	markerOp, _ := statecache.BuildOp(otherID.String(), true)
	w.handleEntity(encodeEntityBatch(t, otherID.String(), 1, [][]byte{markerOp}))
	if !w.Members.Contains(otherID) {
		t.Errorf("after marker: Members should contain otherID")
	}

	ssOp, _ := statecache.BuildOp(otherID.String()+".subspaces."+subA.String(), true)
	w.handleEntity(encodeEntityBatch(t, otherID.String(), 2, [][]byte{ssOp}))
	if set, ok := w.nodeSubspaces[otherID]; !ok || !set.Contains(subA) {
		t.Errorf("after subspace add: nodeSubspaces[other] should contain subA, got %v", w.nodeSubspaces[otherID])
	}

	ssTomb, _ := statecache.BuildTombstoneOp(otherID.String() + ".subspaces." + subA.String())
	w.handleEntity(encodeEntityBatch(t, otherID.String(), 3, [][]byte{ssTomb}))
	if set, ok := w.nodeSubspaces[otherID]; !ok || set.Contains(subA) {
		t.Errorf("after subspace tombstone: subA should be removed, got %v", w.nodeSubspaces[otherID])
	}
	if !w.Members.Contains(otherID) {
		t.Errorf("after subspace tombstone: Members should still contain otherID")
	}

	markerTomb, _ := statecache.BuildTombstoneOp(otherID.String())
	w.handleEntity(encodeEntityBatch(t, otherID.String(), 4, [][]byte{markerTomb}))
	if w.Members.Contains(otherID) {
		t.Errorf("after marker tombstone: Members should not contain otherID")
	}
	if _, ok := w.nodeSubspaces[otherID]; ok {
		t.Errorf("after marker tombstone: nodeSubspaces[other] should be deleted")
	}
}

// TestDataWriterEntityCollisionPrefixRejected mirrors the MOQ collision
// test: a uuid that shares a leading-byte prefix with myID must not be
// treated as visible just because the textual prefix matches.
func TestDataWriterEntityCollisionPrefixRejected(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	collisionID := uuid.MustParse("11111111-1111-1111-1111-111111111122")
	if !strings.HasPrefix(collisionID.String(), "11111111-1111") {
		t.Fatalf("test setup: expected prefix collision")
	}
	w := &DataWriter{MyNodeID: myID, myNodeIDStr: myID.String()}

	if w.entityKeyVisible(collisionID.String() + ".muted") {
		t.Errorf("collision-prefix uuid leaked through filter")
	}
	if !w.entityKeyVisible(myID.String() + ".muted") {
		t.Errorf("self key wrongly rejected")
	}
}

// TestDataWriterSendSpaceCap locks down the WebRTC writer's space
// cap gate: sendSpace short-circuits before any buffer/channel write
// when commands.ReadCapSpaceRead is unset, and admits the envelope
// (buffering since SpaceDataChannel is nil pre-OnOpen) when set.
func TestDataWriterSendSpaceCap(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	op, _ := statecache.BuildOp("roles-muted.performer", true)
	envelope, err := statecache.Encode(statecache.Op{
		Topic: "space",
		Key:   "roles-muted.performer",
		Value: op,
		OpID:  1,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	t.Run("without cap", func(t *testing.T) {
		w := NewDataWriter(myID, nil, nil)
		w.SendString("space", string(envelope))
		if got := len(w.pendingSpace); got != 0 {
			t.Errorf("without space.read cap, expected 0 buffered envelopes; got %d", got)
		}
	})

	t.Run("with cap", func(t *testing.T) {
		w := NewDataWriter(myID, nil, map[string]bool{"space.read": true})
		w.SendString("space", string(envelope))
		if got := len(w.pendingSpace); got != 1 {
			t.Fatalf("with space.read cap, expected 1 buffered envelope; got %d", got)
		}
	})
}
