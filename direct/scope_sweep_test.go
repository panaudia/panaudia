package direct

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/core/statecache"
)

// sendOp pushes a single Set op onto the backend's string channel.
func sendOp(t *testing.T, backend *DirectBackend, topic, key string, value interface{}, source uuid.UUID) {
	t.Helper()
	op, err := statecache.BuildOp(key, value)
	if err != nil {
		t.Fatalf("BuildOp(%q): %v", key, err)
	}
	backend.StringChIn <- StringMessage{topic: topic, msg: string(op), sourceUUID: source}
}

// cacheState returns (present, tombstoned) for a key in the store,
// using the highest-opID entry — Snapshot can return both the ring's
// recent ops and the compacted state for the same key.
func cacheState(backend *DirectBackend, topic, key string) (bool, bool) {
	present := false
	tombstoned := false
	var bestOpID uint64
	for _, op := range backend.Cache.Snapshot() {
		if op.Topic == topic && op.Key == key && op.OpID >= bestOpID {
			bestOpID = op.OpID
			present = true
			tombstoned = op.Tombstone
		}
	}
	return present, tombstoned
}

// waitForCacheSet polls until the key is present (non-tombstoned) in the
// cache — the dispatcher processes StringChIn asynchronously.
func waitForCacheSet(t *testing.T, backend *DirectBackend, topic, key string) {
	t.Helper()
	if !waitFor(time.Second, func() bool {
		present, tomb := cacheState(backend, topic, key)
		return present && !tomb
	}) {
		t.Fatalf("key %q on topic %q never appeared in cache", key, topic)
	}
}

// waitForTombstone polls until the key's latest cache state is a
// tombstone — departure announcements flow through the async dispatcher.
func waitForTombstone(t *testing.T, backend *DirectBackend, topic, key string) {
	t.Helper()
	if !waitFor(time.Second, func() bool {
		present, tomb := cacheState(backend, topic, key)
		return present && tomb
	}) {
		present, tomb := cacheState(backend, topic, key)
		t.Errorf("%s %q should be tombstoned (present=%v tombstoned=%v)", topic, key, present, tomb)
	}
}

// departSubject runs the canonical departure for a uuid: registers an
// entry (as admission would) and invokes DepartNode on it.
func departSubject(t *testing.T, backend *DirectBackend, id uuid.UUID) {
	t.Helper()
	_, entry := backend.Sessions.Register(id, &sessions.FuncSession{}, "test")
	backend.DepartNode(entry, ReasonTransportClosed)
}

// TestModeratorDisconnectLeavesModerationIntact: moderator A mutes,
// kicks, and sets gain on B via the command-shaped entity ops; A
// disconnects (FreeSource). B's moderation keys must survive in both
// the cache and the KickGate — pre-phase-1, authorship-keyed cleanup
// lifted them (findings §2.2).
func TestModeratorDisconnectLeavesModerationIntact(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	backend.KickGate = space.NewKickGate(nil)

	moderator := uuid.New()
	target := uuid.New()

	// What space.entity.mute / .kick / .set_gain emit, with the
	// moderator as the op source (commands flow through the issuing
	// client's bouncer).
	kickTTL := time.Now().Add(time.Hour).UnixMilli()
	sendOp(t, backend, "entity", target.String()+".muted", true, moderator)
	sendOp(t, backend, "entity", target.String()+".kicked", kickTTL, moderator)
	sendOp(t, backend, "entity", target.String()+".gain", 1.5, moderator)
	// The moderator's own connection-scoped state, to give the sweep
	// something legitimate to clear.
	sendOp(t, backend, "attributes", moderator.String()+".name", "mod", moderator)

	waitForCacheSet(t, backend, "entity", target.String()+".gain")
	waitForCacheSet(t, backend, "attributes", moderator.String()+".name")
	if !waitFor(time.Second, func() bool {
		kicked, _ := backend.KickGate.IsKicked(target, nil)
		return kicked
	}) {
		t.Fatal("KickGate never saw the kick")
	}

	// Moderator disconnects (full departure via DepartNode).
	departSubject(t, backend, moderator)

	// The moderator's own attributes were swept...
	waitForTombstone(t, backend, "attributes", moderator.String()+".name")

	// ...while moderation on B persists, in cache...
	for _, key := range []string{".muted", ".kicked", ".gain"} {
		present, tomb := cacheState(backend, "entity", target.String()+key)
		if !present || tomb {
			t.Errorf("entity %s%s should survive the moderator's disconnect (present=%v tombstoned=%v)",
				target, key, present, tomb)
		}
	}
	// ...and in the KickGate.
	if kicked, _ := backend.KickGate.IsKicked(target, nil); !kicked {
		t.Error("KickGate kick lifted by the moderator's disconnect")
	}
}

// TestSubjectDisconnectSweepsConnectionKeysOnly: B's own
// name/ticket/entity keys are tombstoned when B departs, on the right
// topics; B's moderation keys (set by A) persist — policy-scoped keys
// are cleared only by un-commands/TTL, not even by their subject's
// disconnect.
func TestSubjectDisconnectSweepsConnectionKeysOnly(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	moderator := uuid.New()
	b := uuid.New()

	// B's own state: attributes + entity record.
	sendOp(t, backend, "attributes", b.String()+".name", "bee", b)
	sendOp(t, backend, "attributes", b.String()+".ticket", map[string]string{"colour": "#fff"}, b)
	sendOp(t, backend, "entity", b.String(), true, b) // existence marker
	sendOp(t, backend, "entity", b.String()+".roles.performer", true, b)
	// Moderation applied to B by A.
	sendOp(t, backend, "entity", b.String()+".muted", true, moderator)

	waitForCacheSet(t, backend, "entity", b.String()+".muted")
	waitForCacheSet(t, backend, "entity", b.String()+".roles.performer")
	waitForCacheSet(t, backend, "attributes", b.String()+".ticket")

	// Observer to verify the broadcast topics of the sweep.
	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)

	departSubject(t, backend, b)

	// Connection-scoped keys tombstoned…
	for _, tk := range [][2]string{
		{"attributes", b.String() + ".name"},
		{"attributes", b.String() + ".ticket"},
		{"entity", b.String()},
		{"entity", b.String() + ".roles.performer"},
	} {
		waitForTombstone(t, backend, tk[0], tk[1])
	}
	// …policy key persists.
	present, tomb := cacheState(backend, "entity", b.String()+".muted")
	if !present || tomb {
		t.Errorf("entity muted should survive B's own disconnect (present=%v tombstoned=%v)", present, tomb)
	}

	// The sweep broadcasts per topic: one attributes envelope then one
	// entity envelope (attributes first — writers' visibility maps need
	// the entity record alive to route them), and a single Gone=1 on
	// the state topic.
	msgs := observer.waitForStringMsgs(2, time.Second)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 sweep envelopes (attributes, entity), got %d", len(msgs))
	}
	if msgs[0].topic != "attributes" || msgs[1].topic != "entity" {
		t.Errorf("sweep topic order = [%s, %s], want [attributes, entity]", msgs[0].topic, msgs[1].topic)
	}
	dataMsgs := observer.getDataMsgs()
	goneCount := 0
	for _, m := range dataMsgs {
		if m.topic == "state" {
			goneCount++
		}
	}
	if goneCount != 1 {
		t.Errorf("expected exactly 1 Gone state datagram, got %d", goneCount)
	}
}

// TestRoleRulesSurviveDisconnects: space-topic role rules are
// policy-scoped with no subject — no disconnect may clear them.
func TestRoleRulesSurviveDisconnects(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	moderator := uuid.New()
	sendOp(t, backend, "space", "roles-muted.audience", true, moderator)
	sendOp(t, backend, "space", "roles-gain.performer", 2.0, moderator)
	waitForCacheSet(t, backend, "space", "roles-gain.performer")

	departSubject(t, backend, moderator)

	for _, key := range []string{"roles-muted.audience", "roles-gain.performer"} {
		present, tomb := cacheState(backend, "space", key)
		if !present || tomb {
			t.Errorf("space %q should survive the author's disconnect (present=%v tombstoned=%v)",
				key, present, tomb)
		}
	}
}

// TestPersonalPrefsClearedOnAuthorDisconnect: personal preferences
// ({my}.mutes.{x}) are connection-scoped with the VIEWER as subject —
// cleared when the viewer departs.
func TestPersonalPrefsClearedOnAuthorDisconnect(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	viewer := uuid.New()
	other := uuid.New()

	sendOp(t, backend, "entity", viewer.String()+".mutes."+other.String(), true, viewer)
	sendOp(t, backend, "entity", viewer.String()+".mute-roles.audience", true, viewer)
	waitForCacheSet(t, backend, "entity", viewer.String()+".mute-roles.audience")

	departSubject(t, backend, viewer)

	for _, key := range []string{
		viewer.String() + ".mutes." + other.String(),
		viewer.String() + ".mute-roles.audience",
	} {
		waitForTombstone(t, backend, "entity", key)
	}
}

// TestCrossSubjectConnectionWriteDropped: a client writing a
// connection-scoped key under another uuid's prefix is dropped — not
// cached, not broadcast, not tracked (strict-MVC write enforcement).
func TestCrossSubjectConnectionWriteDropped(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()

	attacker := uuid.New()
	victim := uuid.New()

	observerID := uuid.New()
	_, observer := addTestBouncer(backend, observerID)

	// Forged identity write and a legitimate self write in one batch:
	// the batch must be rebuilt with only the legitimate op.
	forged, _ := statecache.BuildOp(victim.String()+".name", "evil")
	legit, _ := statecache.BuildOp(attacker.String()+".name", "me")
	batch, _ := statecache.BuildBatch([][]byte{forged, legit})
	backend.StringChIn <- StringMessage{topic: "attributes", msg: string(batch), sourceUUID: attacker}

	waitForCacheSet(t, backend, "attributes", attacker.String()+".name")

	// Forged key absent from the cache…
	if present, _ := cacheState(backend, "attributes", victim.String()+".name"); present {
		t.Error("cross-subject write reached the cache")
	}
	// …not tracked under the victim…
	backend.Lock()
	_, tracked := backend.ConnKeysBySubject[victim]
	backend.Unlock()
	if tracked {
		t.Error("cross-subject write was tracked under the victim subject")
	}
	// …and the rebuilt broadcast carries only the legitimate op.
	msgs := observer.waitForStringMsgs(1, time.Second)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 broadcast envelope, got %d", len(msgs))
	}
	ops, _, err := statecache.ParseOps([]byte(envelopeValue(t, msgs[0].msg)))
	if err != nil {
		t.Fatalf("parsing rebuilt broadcast: %v", err)
	}
	if len(ops) != 1 || ops[0].Key != attacker.String()+".name" {
		t.Errorf("rebuilt broadcast should carry only the self write, got %+v", ops)
	}

	// A fully-forged single write produces no broadcast at all.
	sendOp(t, backend, "attributes", victim.String()+".connection", "fake", attacker)
	time.Sleep(50 * time.Millisecond)
	if got := len(observer.getStringMsgs()); got != 1 {
		t.Errorf("fully-dropped write still broadcast something (envelopes=%d)", got)
	}

	// Cross-subject POLICY writes are allowed (command path).
	sendOp(t, backend, "entity", victim.String()+".muted", true, attacker)
	waitForCacheSet(t, backend, "entity", victim.String()+".muted")
}

// envelopeValue extracts the inner op payload from a cache envelope.
func envelopeValue(t *testing.T, msg string) string {
	t.Helper()
	op, err := statecache.Decode([]byte(msg))
	if err != nil {
		t.Fatalf("decoding envelope: %v", err)
	}
	return string(op.Value)
}
