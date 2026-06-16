package space

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/statecache"
)

// fakeBouncer is a stub IMessageSenderReceiver that records every
// SendString call so tests can assert on the exact entity-topic
// payloads emitted by BouncerClient.
type fakeBouncer struct {
	mu   sync.Mutex
	sent []sentString
}

type sentString struct {
	topic string
	msg   string
}

func (f *fakeBouncer) SendString(topic, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentString{topic: topic, msg: msg})
}
func (f *fakeBouncer) SendData(topic string, data []byte)            {}
func (f *fakeBouncer) SetReceiveSender(receiveSender IMessageSender) {}

// onTopic returns the messages the fake bouncer received for the given
// topic, taking a defensive copy so the caller can iterate without
// holding the lock.
func (f *fakeBouncer) onTopic(topic string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sent))
	for _, s := range f.sent {
		if s.topic == topic {
			out = append(out, s.msg)
		}
	}
	return out
}

// waitForEntityMessage polls until at least one envelope arrives on
// the "entity" topic, or the deadline expires. Returns the first such
// envelope as raw bytes (the inner JSON op or batch).
func waitForEntityMessage(t *testing.T, bouncer *fakeBouncer) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		msgs := bouncer.onTopic("entity")
		if len(msgs) >= 1 {
			return []byte(msgs[0])
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for entity message")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// expectKeysInBatch decodes a batch envelope and asserts the set of
// non-tombstone keys it contains exactly matches `want`. Order is
// irrelevant — sendEntity makes no order guarantee, only that the keys
// are all present.
func expectKeysInBatch(t *testing.T, msg []byte, want map[string]bool) {
	t.Helper()
	ops, _, err := statecache.ParseOps(msg)
	if err != nil {
		t.Fatalf("ParseOps: %v", err)
	}
	if len(ops) != len(want) {
		t.Fatalf("expected %d ops, got %d: %v", len(want), len(ops), ops)
	}
	for _, op := range ops {
		if op.Tombstone {
			t.Errorf("unexpected tombstone in initial sendEntity: %s", op.Key)
		}
		if _, ok := want[op.Key]; !ok {
			t.Errorf("unexpected key in entity batch: %q", op.Key)
			continue
		}
		want[op.Key] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing expected key: %q", k)
		}
	}
}

// TestSendEntityFlatKeySchema constructs a BouncerClient with two
// subspaces and asserts the emitted entity batch contains exactly:
//   - one existence-marker op at `{uuid}` with value `true`,
//   - one op per subspace at `{uuid}.subspaces.{ss}` with value `true`.
//
// This locks down the Phase 1 flat-key wire format end-to-end.
func TestSendEntityFlatKeySchema(t *testing.T) {
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	ssA := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	ssB := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	bouncer := &fakeBouncer{}
	cfg := common.NodeConfig{
		Uuid: myID,
		Name: "test",
		SpaceNodeConfig: common.SpaceNodeConfig{
			SubSpaces: []uuid.UUID{ssA, ssB},
		},
	}
	client := NewBouncerClient(cfg, bouncer)
	defer client.Depart()

	msg := waitForEntityMessage(t, bouncer)
	expectKeysInBatch(t, msg, map[string]bool{
		myID.String(): false,
		myID.String() + ".subspaces." + ssA.String(): false,
		myID.String() + ".subspaces." + ssB.String(): false,
	})
}

// TestSendEntityIncludesRoles checks that ticket-supplied roles flow
// through into the entity stream as `{uuid}.roles.{role}` set ops,
// alongside the existing marker + subspaces. Without this, role-aware
// commands and UI have no source of truth for which entities hold
// which role.
func TestSendEntityIncludesRoles(t *testing.T) {
	myID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	ssA := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	bouncer := &fakeBouncer{}
	cfg := common.NodeConfig{
		Uuid:  myID,
		Name:  "test",
		Roles: []string{"performer", "moderator"},
		SpaceNodeConfig: common.SpaceNodeConfig{
			SubSpaces: []uuid.UUID{ssA},
		},
	}
	client := NewBouncerClient(cfg, bouncer)
	defer client.Depart()

	msg := waitForEntityMessage(t, bouncer)
	expectKeysInBatch(t, msg, map[string]bool{
		myID.String(): false,
		myID.String() + ".subspaces." + ssA.String(): false,
		myID.String() + ".roles.performer":           false,
		myID.String() + ".roles.moderator":           false,
	})
}

// TestSendEntitySkipsEmptyRoleStrings — defensive: an empty string in
// the roles slice (e.g. trailing comma in a config file) must not
// produce a `{uuid}.roles.` key with an empty trailing segment, which
// would collide with any future bare-`roles` field.
func TestSendEntitySkipsEmptyRoleStrings(t *testing.T) {
	myID := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	bouncer := &fakeBouncer{}
	cfg := common.NodeConfig{
		Uuid:  myID,
		Name:  "test",
		Roles: []string{"performer", "", "audience"},
	}
	client := NewBouncerClient(cfg, bouncer)
	defer client.Depart()

	msg := waitForEntityMessage(t, bouncer)
	expectKeysInBatch(t, msg, map[string]bool{
		myID.String():                      false,
		myID.String() + ".roles.performer": false,
		myID.String() + ".roles.audience":  false,
	})
}

// TestRepublishReemitsState: Republish (the bouncer-reconnect hook,
// phase 6b) immediately re-emits the entity record and attributes; after
// Quiesce it emits nothing — a departed node must not resurrect from a
// late reconnect callback.
func TestRepublishReemitsState(t *testing.T) {
	myID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	bouncer := &fakeBouncer{}
	cfg := common.NodeConfig{Uuid: myID, Name: "republisher"}
	client := NewBouncerClient(cfg, bouncer)
	defer client.Depart()

	// Initial emit from construction.
	waitForEntityMessage(t, bouncer)
	before := len(bouncer.onTopic("entity"))

	client.Republish()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(bouncer.onTopic("entity")) > before {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(bouncer.onTopic("entity")); got <= before {
		t.Fatalf("Republish did not re-emit the entity record (entity msgs: %d)", got)
	}
	if got := len(bouncer.onTopic("attributes")); got < 2 {
		t.Fatalf("Republish did not re-emit attributes (attribute msgs: %d)", got)
	}

	// After quiesce: silence.
	client.Quiesce()
	afterEntity := len(bouncer.onTopic("entity"))
	afterAttrs := len(bouncer.onTopic("attributes"))
	client.Republish()
	time.Sleep(50 * time.Millisecond)
	if len(bouncer.onTopic("entity")) != afterEntity || len(bouncer.onTopic("attributes")) != afterAttrs {
		t.Error("Republish emitted after Quiesce — departed node would resurrect")
	}
}
