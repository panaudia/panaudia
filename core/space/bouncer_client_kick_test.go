package space

import (
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/statecache"
)

// kickedClient builds a BouncerClient wired up with a counting kickFn and
// returns the client + the counter pointer. Callers should `defer
// client.Stop()` and read the counter via atomic.LoadInt32.
func kickedClient(myID uuid.UUID, roles []string) (*BouncerClient, *int32) {
	bouncer := &fakeBouncer{}
	cfg := common.NodeConfig{
		Uuid:  myID,
		Name:  "test",
		Roles: roles,
	}
	client := NewBouncerClient(cfg, bouncer)
	var counter int32
	client.SetKickFn(func() {
		atomic.AddInt32(&counter, 1)
	})
	return client, &counter
}

// envelopeFor wraps a single op JSON in a cache envelope, the same
// shape the broadcast loop sends on the wire.
func envelopeFor(t *testing.T, topic, key string, value interface{}, opID uint64, tombstone bool) []byte {
	t.Helper()
	var inner []byte
	var err error
	if tombstone {
		inner, err = statecache.BuildTombstoneOp(key)
	} else {
		inner, err = statecache.BuildOp(key, value)
	}
	if err != nil {
		t.Fatalf("build op: %v", err)
	}
	envelope := statecache.Op{
		Topic:     topic,
		Key:       key,
		Value:     inner,
		OpID:      opID,
		Tombstone: tombstone,
	}
	encoded, err := statecache.Encode(envelope)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	return encoded
}

// envelopeForBatch wraps several inner ops in one cache envelope. The
// envelope-level Tombstone is left false; per-op tombstones are set
// inside the inner ops.
func envelopeForBatch(t *testing.T, topic string, ops [][]byte, opID uint64) []byte {
	t.Helper()
	batch, err := statecache.BuildBatch(ops)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	envelope := statecache.Op{
		Topic: topic,
		Key:   topic + "-batch",
		Value: batch,
		OpID:  opID,
	}
	encoded, err := statecache.Encode(envelope)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return encoded
}

func TestKickFnFiresOnEntityKick(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	client, counter := kickedClient(myID, nil)
	defer client.Depart()

	env := envelopeFor(t, "entity", myID.String()+".kicked", float64(60_000), 1, false)
	client.SendString("entity", string(env))

	if got := atomic.LoadInt32(counter); got != 1 {
		t.Fatalf("kickFn called %d times, want 1", got)
	}
}

func TestKickFnFiresOnRoleKick(t *testing.T) {
	myID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	client, counter := kickedClient(myID, []string{"performer", "audience"})
	defer client.Depart()

	env := envelopeFor(t, "space", "roles-kicked.audience", float64(60_000), 1, false)
	client.SendString("space", string(env))

	if got := atomic.LoadInt32(counter); got != 1 {
		t.Fatalf("kickFn called %d times, want 1", got)
	}
}

// A kick aimed at someone else must NOT fire our kickFn.
func TestKickFnIgnoresOtherUuidKick(t *testing.T) {
	myID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	otherID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	client, counter := kickedClient(myID, nil)
	defer client.Depart()

	env := envelopeFor(t, "entity", otherID.String()+".kicked", float64(60_000), 1, false)
	client.SendString("entity", string(env))

	if got := atomic.LoadInt32(counter); got != 0 {
		t.Fatalf("kickFn should not fire for another uuid; got %d calls", got)
	}
}

// Role-kick for a role we don't hold must NOT fire.
func TestKickFnIgnoresRoleNotHeld(t *testing.T) {
	myID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	client, counter := kickedClient(myID, []string{"audience"})
	defer client.Depart()

	env := envelopeFor(t, "space", "roles-kicked.performer", float64(60_000), 1, false)
	client.SendString("space", string(env))

	if got := atomic.LoadInt32(counter); got != 0 {
		t.Fatalf("kickFn should not fire for unheld role; got %d calls", got)
	}
}

// Tombstone kick = "unkick" — must NOT fire.
func TestKickFnIgnoresTombstoneEntityKick(t *testing.T) {
	myID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	client, counter := kickedClient(myID, nil)
	defer client.Depart()

	env := envelopeFor(t, "entity", myID.String()+".kicked", nil, 1, true)
	client.SendString("entity", string(env))

	if got := atomic.LoadInt32(counter); got != 0 {
		t.Fatalf("kickFn should not fire for tombstone (unkick); got %d calls", got)
	}
}

func TestKickFnIgnoresTombstoneRoleKick(t *testing.T) {
	myID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	client, counter := kickedClient(myID, []string{"audience"})
	defer client.Depart()

	env := envelopeFor(t, "space", "roles-kicked.audience", nil, 1, true)
	client.SendString("space", string(env))

	if got := atomic.LoadInt32(counter); got != 0 {
		t.Fatalf("kickFn should not fire for tombstone (unkick); got %d calls", got)
	}
}

// A batch envelope mixing a kick op alongside unrelated ops should
// still trigger exactly one kickFn call (single-fire).
func TestKickFnSingleFireInBatch(t *testing.T) {
	myID := uuid.MustParse("88888888-8888-8888-8888-888888888888")
	client, counter := kickedClient(myID, nil)
	defer client.Depart()

	gainOp, _ := statecache.BuildOp(myID.String()+".gain", 0.5)
	kickOp, _ := statecache.BuildOp(myID.String()+".kicked", float64(60_000))
	muteOp, _ := statecache.BuildOp(myID.String()+".muted", true)

	env := envelopeForBatch(t, "entity", [][]byte{gainOp, kickOp, muteOp}, 42)
	client.SendString("entity", string(env))

	if got := atomic.LoadInt32(counter); got != 1 {
		t.Fatalf("kickFn called %d times, want 1", got)
	}
}

// Repeated kick deliveries (same envelope twice, or fresh kick op
// after the first one) must still fire kickFn exactly once. The
// connection is being torn down anyway.
func TestKickFnSingleFireOnRepeatedKick(t *testing.T) {
	myID := uuid.MustParse("99999999-9999-9999-9999-999999999999")
	client, counter := kickedClient(myID, nil)
	defer client.Depart()

	env1 := envelopeFor(t, "entity", myID.String()+".kicked", float64(60_000), 1, false)
	env2 := envelopeFor(t, "entity", myID.String()+".kicked", float64(120_000), 2, false)
	client.SendString("entity", string(env1))
	client.SendString("entity", string(env2))
	client.SendString("entity", string(env1))

	if got := atomic.LoadInt32(counter); got != 1 {
		t.Fatalf("kickFn called %d times, want 1", got)
	}
}

// Without SetKickFn installed, a kick op is silently no-op'd —
// matching the documented late-binding behaviour.
func TestKickFnNoFnInstalled(t *testing.T) {
	myID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	bouncer := &fakeBouncer{}
	cfg := common.NodeConfig{Uuid: myID, Name: "test"}
	client := NewBouncerClient(cfg, bouncer)
	defer client.Depart()

	// no SetKickFn — must not panic, must silently drop.
	env := envelopeFor(t, "entity", myID.String()+".kicked", float64(60_000), 1, false)
	client.SendString("entity", string(env))
}

// Non-envelope payloads on entity / space topics (e.g. a future
// gateway misconfiguration sending bare JSON) must not fire kickFn.
func TestKickFnIgnoresBareJson(t *testing.T) {
	myID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	client, counter := kickedClient(myID, nil)
	defer client.Depart()

	bare, _ := statecache.BuildOp(myID.String()+".kicked", float64(60_000))
	client.SendString("entity", string(bare))

	if got := atomic.LoadInt32(counter); got != 0 {
		t.Fatalf("kickFn should not fire on bare-JSON payload; got %d calls", got)
	}
}

// Topics other than entity / space never trigger kicks — even if a
// kick-shaped key happens to ride past on a different topic.
func TestKickFnIgnoresOtherTopics(t *testing.T) {
	myID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	client, counter := kickedClient(myID, nil)
	defer client.Depart()

	env := envelopeFor(t, "attributes", myID.String()+".kicked", float64(60_000), 1, false)
	client.SendString("attributes", string(env))

	if got := atomic.LoadInt32(counter); got != 0 {
		t.Fatalf("kickFn should not fire on non-entity/space topic; got %d calls", got)
	}
}
