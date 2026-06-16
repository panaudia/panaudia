package direct

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/core/statecache"
)

// captureSpace embeds *space.BaseSpace and records every Apply*Op call so
// the Phase 4 tee can be verified end-to-end without standing up a full
// audio loop. The embedded BaseSpace still receives the ops (and they
// land in its op queue) — we just observe the call pre-forward.
type captureSpace struct {
	*space.BaseSpace
	mu        sync.Mutex
	entityOps []statecache.Op
	spaceOps  []statecache.Op
}

func newCaptureSpace() *captureSpace {
	bs := space.NewBaseSpace("test", 10.0, 1, 16, 400, 0)
	return &captureSpace{BaseSpace: &bs}
}

func (c *captureSpace) ApplyEntityOp(op statecache.Op) {
	c.mu.Lock()
	c.entityOps = append(c.entityOps, op)
	c.mu.Unlock()
	c.BaseSpace.ApplyEntityOp(op)
}

func (c *captureSpace) ApplySpaceOp(op statecache.Op) {
	c.mu.Lock()
	c.spaceOps = append(c.spaceOps, op)
	c.mu.Unlock()
	c.BaseSpace.ApplySpaceOp(op)
}

func (c *captureSpace) waitForEntityOps(n int, timeout time.Duration) []statecache.Op {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := len(c.entityOps)
		c.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]statecache.Op, len(c.entityOps))
	copy(out, c.entityOps)
	return out
}

func (c *captureSpace) waitForSpaceOps(n int, timeout time.Duration) []statecache.Op {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := len(c.spaceOps)
		c.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]statecache.Op, len(c.spaceOps))
	copy(out, c.spaceOps)
	return out
}

func newTestBackendWithSpace(t *testing.T) (*DirectBackend, *captureSpace) {
	t.Helper()
	backend := newTestBackend()
	cs := newCaptureSpace()
	backend.SetSpace(cs)
	return backend, cs
}

// TestApplyOpTee_EntitySingle confirms a single entity op sent through
// the backend reaches space.ApplyEntityOp with the inner value bytes
// (per the Phase 3 convention).
func TestApplyOpTee_EntitySingle(t *testing.T) {
	backend, cs := newTestBackendWithSpace(t)
	t.Cleanup(func() { close(backend.Quit) })

	id := uuid.New()
	key := id.String() + ".gain"
	src := uuid.New()
	op, err := statecache.BuildOp(key, 0.5)
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}

	backend.StringChIn <- StringMessage{topic: "entity", msg: string(op), sourceUUID: src}

	got := cs.waitForEntityOps(1, time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 entity op tee'd, got %d", len(got))
	}
	if got[0].Key != key {
		t.Fatalf("tee'd op key = %q, want %q", got[0].Key, key)
	}
	if got[0].Tombstone {
		t.Fatal("tee'd op should not be a tombstone")
	}
	// Phase 3 convention: Op.Value is the JSON of the value field only.
	if string(got[0].Value) != "0.5" {
		t.Fatalf("tee'd op value = %q, want \"0.5\"", got[0].Value)
	}
}

// TestApplyOpTee_EntityTombstone verifies tombstone ops carry no value
// and the tombstone flag is preserved across the tee.
func TestApplyOpTee_EntityTombstone(t *testing.T) {
	backend, cs := newTestBackendWithSpace(t)
	t.Cleanup(func() { close(backend.Quit) })

	id := uuid.New()
	key := id.String() + ".muted"
	op, err := statecache.BuildTombstoneOp(key)
	if err != nil {
		t.Fatalf("BuildTombstoneOp: %v", err)
	}

	backend.StringChIn <- StringMessage{topic: "entity", msg: string(op), sourceUUID: uuid.New()}

	got := cs.waitForEntityOps(1, time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 tee'd op, got %d", len(got))
	}
	if !got[0].Tombstone {
		t.Fatal("expected tombstone flag preserved on tee")
	}
	if len(got[0].Value) != 0 {
		t.Fatalf("tombstone op should have empty value, got %q", got[0].Value)
	}
}

// TestApplyOpTee_SpaceTopic confirms space-topic ops route to ApplySpaceOp.
func TestApplyOpTee_SpaceTopic(t *testing.T) {
	backend, cs := newTestBackendWithSpace(t)
	t.Cleanup(func() { close(backend.Quit) })

	op, err := statecache.BuildOp("roles-gain.performer", 0.25)
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	backend.StringChIn <- StringMessage{topic: "space", msg: string(op), sourceUUID: uuid.New()}

	got := cs.waitForSpaceOps(1, time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 space op, got %d", len(got))
	}
	if got[0].Key != "roles-gain.performer" {
		t.Fatalf("space op key = %q, want \"roles-gain.performer\"", got[0].Key)
	}
	if string(got[0].Value) != "0.25" {
		t.Fatalf("space op value = %q, want \"0.25\"", got[0].Value)
	}
}

// TestApplyOpTee_OtherTopicSkipped confirms a non-entity/space topic
// (e.g. "attributes") is cached but NOT tee'd into Apply* methods.
func TestApplyOpTee_OtherTopicSkipped(t *testing.T) {
	backend, cs := newTestBackendWithSpace(t)
	t.Cleanup(func() { close(backend.Quit) })

	op, err := statecache.BuildOp(uuid.New().String()+".name", "alice")
	if err != nil {
		t.Fatalf("BuildOp: %v", err)
	}
	backend.StringChIn <- StringMessage{topic: "attributes", msg: string(op), sourceUUID: uuid.New()}

	// Allow the goroutine a beat to process; the tee shouldn't fire.
	time.Sleep(20 * time.Millisecond)

	cs.mu.Lock()
	if got := len(cs.entityOps); got != 0 {
		t.Fatalf("attributes topic should not tee to entity, got %d", got)
	}
	if got := len(cs.spaceOps); got != 0 {
		t.Fatalf("attributes topic should not tee to space, got %d", got)
	}
	cs.mu.Unlock()
}

// TestApplyOpTee_BatchOps confirms a batch envelope produces one tee'd
// Op per inner op, all sharing the same OpID (the envelope's).
func TestApplyOpTee_BatchOps(t *testing.T) {
	backend, cs := newTestBackendWithSpace(t)
	t.Cleanup(func() { close(backend.Quit) })

	id := uuid.New()
	op1, _ := statecache.BuildOp(id.String()+".gain", 0.7)
	op2, _ := statecache.BuildOp(id.String()+".attenuation", 3.0)
	op3, _ := statecache.BuildOp(id.String()+".muted", true)
	batch, err := statecache.BuildBatch([][]byte{op1, op2, op3})
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	backend.StringChIn <- StringMessage{topic: "entity", msg: string(batch), sourceUUID: uuid.New()}

	got := cs.waitForEntityOps(3, time.Second)
	if len(got) != 3 {
		t.Fatalf("expected 3 entity ops tee'd, got %d", len(got))
	}
	// All ops in a batch share the envelope's OpID.
	if got[0].OpID != got[1].OpID || got[1].OpID != got[2].OpID {
		t.Fatalf("batch ops should share OpID; got %d, %d, %d", got[0].OpID, got[1].OpID, got[2].OpID)
	}
}

// TestApplyOpTee_NilSpaceTolerated guards the early-return when ISpace
// hasn't been wired (e.g. the cache-only test backend).
func TestApplyOpTee_NilSpaceTolerated(t *testing.T) {
	backend := newTestBackend()
	t.Cleanup(func() { close(backend.Quit) })
	if backend.ISpace != nil {
		t.Fatal("test backend should have nil ISpace by default")
	}

	id := uuid.New()
	op, _ := statecache.BuildOp(id.String()+".gain", 0.5)
	// Must not panic.
	backend.StringChIn <- StringMessage{topic: "entity", msg: string(op), sourceUUID: uuid.New()}

	// Give the goroutine a moment; we just want to confirm no panic.
	time.Sleep(20 * time.Millisecond)
}
