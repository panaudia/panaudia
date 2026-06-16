package space

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/ambisonic"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/statecache"
)

func mkEncoder(slot int) *ambisonic.Encoder {
	cfg := common.MixerConfig{
		MaxNodes:     8,
		FrameSize:    8,
		ChannelCount: 9,
		Order:        2,
		Size:         2,
		ReverbPreset: common.REVERB_NONE,
	}
	return ambisonic.NewEncoder(uuid.New(), false, 1.0, 2.0, cfg, slot)
}

// addTestNode bypasses the SourceManager/changesQueue path and directly
// installs a Node with its own Encoder. Sufficient for op-apply tests
// that only touch encoder + role state.
func addTestNode(t *testing.T, s *BaseSpace, gain, attenuation float64) *Node {
	t.Helper()
	id := uuid.New()
	cfg := common.SpaceNodeConfig{
		Gain:        gain,
		Attenuation: attenuation,
	}
	mixerCfg := common.MixerConfig{
		MaxNodes:     8,
		FrameSize:    8,
		ChannelCount: 9,
		Order:        2,
		Size:         2,
		ReverbPreset: common.REVERB_NONE,
	}
	slot := len(s.Nodes)
	encoder := ambisonic.NewEncoder(id, false, gain, attenuation, mixerCfg, slot)
	node := &Node{
		Uuid:    id,
		Cube:    s,
		config:  cfg,
		Encoder: encoder,
	}
	s.Nodes[id] = node
	return node
}

func gainFactor(gain float64) float32 {
	return float32(math.Pow(gain, 0.5)) * ambisonic.SQRT_4_PI
}

func newTestSpace() *BaseSpace {
	s := NewBaseSpace("test", 10.0, 1, 16, 400, 0)
	return &s
}

// TestApplyEntityOpDedupHigherWins covers the dedup rule on a single
// (topic, key) pair: regardless of arrival order, the highest OpID survives.
func TestApplyEntityOpDedupHigherWins(t *testing.T) {
	cases := []struct {
		name  string
		first statecache.Op
		later statecache.Op
		want  uint64
	}{
		{
			name:  "later op has higher id",
			first: statecache.Op{Topic: "entity", Key: "abc.gain", OpID: 1, Value: []byte("0.5")},
			later: statecache.Op{Topic: "entity", Key: "abc.gain", OpID: 7, Value: []byte("0.9")},
			want:  7,
		},
		{
			name:  "later op has lower id (must NOT replace)",
			first: statecache.Op{Topic: "entity", Key: "abc.gain", OpID: 7, Value: []byte("0.9")},
			later: statecache.Op{Topic: "entity", Key: "abc.gain", OpID: 1, Value: []byte("0.5")},
			want:  7,
		},
		{
			name:  "tombstone wins over earlier set",
			first: statecache.Op{Topic: "entity", Key: "abc.muted", OpID: 4, Value: []byte("true")},
			later: statecache.Op{Topic: "entity", Key: "abc.muted", OpID: 9, Tombstone: true},
			want:  9,
		},
		{
			name:  "set wins over earlier tombstone",
			first: statecache.Op{Topic: "entity", Key: "abc.muted", OpID: 4, Tombstone: true},
			later: statecache.Op{Topic: "entity", Key: "abc.muted", OpID: 9, Value: []byte("true")},
			want:  9,
		},
		{
			name:  "equal OpID: later replaces (>= matches Cache.Set LWW)",
			first: statecache.Op{Topic: "entity", Key: "abc.gain", OpID: 5, Value: []byte("0.5")},
			later: statecache.Op{Topic: "entity", Key: "abc.gain", OpID: 5, Value: []byte("0.9")},
			want:  5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSpace()
			s.ApplyEntityOp(tc.first)
			s.ApplyEntityOp(tc.later)

			if got := len(s.opChanges); got != 1 {
				t.Fatalf("expected 1 entry in opChanges, got %d", got)
			}
			got := s.opChanges[opKey{topic: "entity", key: tc.first.Key}]
			if got.OpID != tc.want {
				t.Fatalf("expected surviving OpID %d, got %d", tc.want, got.OpID)
			}
			// equal-OpID case: later must replace, so check value too
			if tc.name == "equal OpID: later replaces (>= matches Cache.Set LWW)" {
				if string(got.Value) != "0.9" {
					t.Fatalf("equal-OpID: expected later value '0.9', got %q", got.Value)
				}
			}
		})
	}
}

// TestApplyOpEntityVsSpaceTopicSeparation verifies the (topic, key) tuple
// keeps entity and space ops with the same key from colliding.
func TestApplyOpEntityVsSpaceTopicSeparation(t *testing.T) {
	s := newTestSpace()
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "abc", OpID: 1})
	s.ApplySpaceOp(statecache.Op{Topic: "space", Key: "abc", OpID: 2})

	if got := len(s.opChanges); got != 2 {
		t.Fatalf("expected 2 entries (one per topic), got %d", got)
	}
}

// TestApplyOpDifferentKeysCoexist makes sure the queue holds independent
// ops without conflating them.
func TestApplyOpDifferentKeysCoexist(t *testing.T) {
	s := newTestSpace()
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "a.gain", OpID: 1})
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "b.gain", OpID: 2})
	s.ApplySpaceOp(statecache.Op{Topic: "space", Key: "roles-gain.performer", OpID: 3})

	if got := len(s.opChanges); got != 3 {
		t.Fatalf("expected 3 entries, got %d", got)
	}
}

// TestProcessOpChangesDrains confirms the drain empties the queue and is a
// no-op on an already-empty queue.
func TestProcessOpChangesDrains(t *testing.T) {
	s := newTestSpace()
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "a.gain", OpID: 1})
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "b.gain", OpID: 2})
	s.ApplySpaceOp(statecache.Op{Topic: "space", Key: "roles-muted.performer", OpID: 3})

	if got := len(s.opChanges); got != 3 {
		t.Fatalf("pre-drain: expected 3 entries, got %d", got)
	}

	s.processOpChanges()

	if got := len(s.opChanges); got != 0 {
		t.Fatalf("post-drain: expected empty queue, got %d entries", got)
	}

	// Re-drain on empty queue: no panic, no side effect.
	s.processOpChanges()
	if got := len(s.opChanges); got != 0 {
		t.Fatalf("second drain: expected empty queue, got %d entries", got)
	}
}

// --- RefreshEncoderRoleEffects ----------------------------------------

func TestRefreshEncoderRoleEffectsNoRolesIsIdentity(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	// No roles set, no space role state.
	s.RefreshEncoderRoleEffects(e)
	if got := e.GainFactor(); got != gainFactor(1.0) {
		t.Fatalf("expected gainFactor for gain=1.0, got %v", got)
	}
	if got := e.PeerAttenuationExponent(); got != 1.0 {
		t.Fatalf("expected exponent 1.0 (att=2.0/2), got %v", got)
	}
	if e.SpaceRoleMuted() {
		t.Fatal("expected spaceRoleMuted=false")
	}
}

func TestRefreshEncoderRoleEffectsRoleMute(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("performer")
	s.mutedRoles.Add("performer")

	s.RefreshEncoderRoleEffects(e)
	if !e.SpaceRoleMuted() {
		t.Fatal("expected spaceRoleMuted=true after role added to mutedRoles")
	}

	// Tombstone-equivalent: drop role from mutedRoles and refresh.
	s.mutedRoles.Remove("performer")
	s.RefreshEncoderRoleEffects(e)
	if e.SpaceRoleMuted() {
		t.Fatal("expected spaceRoleMuted=false after role removed from mutedRoles")
	}
}

func TestRefreshEncoderRoleEffectsRoleMuteUnrelatedRole(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("audience")
	s.mutedRoles.Add("performer") // disjoint
	s.RefreshEncoderRoleEffects(e)
	if e.SpaceRoleMuted() {
		t.Fatal("disjoint roles must not flag spaceRoleMuted")
	}
}

func TestRefreshEncoderRoleEffectsSingleRoleGain(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("performer")
	s.roleGains["performer"] = 0.25

	s.RefreshEncoderRoleEffects(e)
	// effective gain = 1.0 (entity) × 0.25 (role) = 0.25
	if got, want := e.GainFactor(), gainFactor(0.25); got != want {
		t.Fatalf("single-role gain composition: got %v, want %v", got, want)
	}
}

func TestRefreshEncoderRoleEffectsMultiRoleGainTakesMin(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("performer")
	e.AddRole("moderator")
	s.roleGains["performer"] = 0.8
	s.roleGains["moderator"] = 0.3 // smaller wins

	s.RefreshEncoderRoleEffects(e)
	if got, want := e.GainFactor(), gainFactor(0.3); got != want {
		t.Fatalf("multi-role gain (min wins): got %v, want %v", got, want)
	}
}

func TestRefreshEncoderRoleEffectsRoleGainClearedRestoresEntity(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("performer")
	s.roleGains["performer"] = 0.5
	s.RefreshEncoderRoleEffects(e)
	delete(s.roleGains, "performer") // tombstone-equivalent
	s.RefreshEncoderRoleEffects(e)
	if got, want := e.GainFactor(), gainFactor(1.0); got != want {
		t.Fatalf("after role-gain cleared: got %v, want %v", got, want)
	}
}

func TestRefreshEncoderRoleEffectsRoleAttenuationOverride(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("performer")
	s.roleAttenuations["performer"] = 6.0 // override → exponent 3.0

	s.RefreshEncoderRoleEffects(e)
	if got := e.PeerAttenuationExponent(); got != 3.0 {
		t.Fatalf("role-attenuation override: expected 3.0, got %v", got)
	}
}

func TestRefreshEncoderRoleEffectsRoleAttenuationMultiRoleMin(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("a")
	e.AddRole("b")
	s.roleAttenuations["a"] = 6.0
	s.roleAttenuations["b"] = 4.0 // smaller wins

	s.RefreshEncoderRoleEffects(e)
	if got := e.PeerAttenuationExponent(); got != 2.0 {
		t.Fatalf("multi-role attenuation (min wins, exp=4/2): expected 2.0, got %v", got)
	}
}

func TestRefreshEncoderRoleEffectsRoleAttenuationClearedRestoresEntity(t *testing.T) {
	s := newTestSpace()
	e := mkEncoder(0)
	e.AddRole("performer")
	s.roleAttenuations["performer"] = 6.0
	s.RefreshEncoderRoleEffects(e)
	delete(s.roleAttenuations, "performer")
	s.RefreshEncoderRoleEffects(e)
	if got := e.PeerAttenuationExponent(); got != 1.0 {
		t.Fatalf("after role-att cleared: expected 1.0 (entity att=2.0/2), got %v", got)
	}
}

// TestProcessOpChangesAfterDedup verifies that a sequence of redundant
// enqueues followed by drain dispatches each (topic, key) exactly once
// with the highest-OpID payload. We assert via the post-drain queue state
// (Phase 3 adds state-mutation assertions).
func TestProcessOpChangesAfterDedup(t *testing.T) {
	s := newTestSpace()

	// Same key, ascending IDs.
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "x.gain", OpID: 1, Value: []byte("0.1")})
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "x.gain", OpID: 2, Value: []byte("0.2")})
	s.ApplyEntityOp(statecache.Op{Topic: "entity", Key: "x.gain", OpID: 3, Value: []byte("0.3")})

	// Same key, out-of-order arrival.
	s.ApplySpaceOp(statecache.Op{Topic: "space", Key: "roles-gain.r", OpID: 9, Value: []byte("0.9")})
	s.ApplySpaceOp(statecache.Op{Topic: "space", Key: "roles-gain.r", OpID: 4, Value: []byte("0.4")})

	// Pre-drain: dedup leaves one entry per (topic, key) with highest OpID.
	if got := len(s.opChanges); got != 2 {
		t.Fatalf("pre-drain: expected 2 entries after dedup, got %d", got)
	}
	if op := s.opChanges[opKey{topic: "entity", key: "x.gain"}]; op.OpID != 3 {
		t.Fatalf("entity x.gain: expected OpID 3, got %d", op.OpID)
	}
	if op := s.opChanges[opKey{topic: "space", key: "roles-gain.r"}]; op.OpID != 9 {
		t.Fatalf("space roles-gain.r: expected OpID 9, got %d", op.OpID)
	}

	s.processOpChanges()

	if got := len(s.opChanges); got != 0 {
		t.Fatalf("post-drain: expected empty queue, got %d entries", got)
	}
}

// ─── Phase 3: per-leaf mutator tests ────────────────────────────────────

func entityKey(id uuid.UUID, suffix string) string {
	if suffix == "" {
		return id.String()
	}
	return id.String() + "." + suffix
}

func TestApplyEntityOpGainSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)

	// Set gain to 0.5
	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "gain"),
		Value: []byte("0.5"),
	})
	if got := n.Encoder.GainFactor(); got != gainFactor(0.5) {
		t.Fatalf("set gain 0.5: gainFactor=%v, want %v", got, gainFactor(0.5))
	}

	// Tombstone restores config.Gain (1.0)
	s.applyEntityOp(statecache.Op{
		Topic:     "entity",
		Key:       entityKey(n.Uuid, "gain"),
		Tombstone: true,
	})
	if got := n.Encoder.GainFactor(); got != gainFactor(1.0) {
		t.Fatalf("tombstone gain: gainFactor=%v, want %v", got, gainFactor(1.0))
	}
}

func TestApplyEntityOpAttenuationSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)

	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "attenuation"),
		Value: []byte("4.0"),
	})
	if got := n.Encoder.PeerAttenuationExponent(); got != 2.0 {
		t.Fatalf("set att 4.0: exponent=%v, want 2.0", got)
	}

	s.applyEntityOp(statecache.Op{
		Topic:     "entity",
		Key:       entityKey(n.Uuid, "attenuation"),
		Tombstone: true,
	})
	if got := n.Encoder.PeerAttenuationExponent(); got != 1.0 {
		t.Fatalf("tombstone att: exponent=%v, want 1.0 (config 2.0/2)", got)
	}
}

func TestApplyEntityOpMutedSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)

	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "muted"),
		Value: []byte("true"),
	})
	if !n.Encoder.SpaceMuted {
		t.Fatal("set muted: expected SpaceMuted=true")
	}

	s.applyEntityOp(statecache.Op{
		Topic:     "entity",
		Key:       entityKey(n.Uuid, "muted"),
		Tombstone: true,
	})
	if n.Encoder.SpaceMuted {
		t.Fatal("tombstone muted: expected SpaceMuted=false")
	}
}

func TestApplyEntityOpRoleSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)

	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "roles.performer"),
		Value: []byte("true"),
	})
	if !n.Encoder.Roles.Contains("performer") {
		t.Fatal("set role: expected role 'performer'")
	}

	s.applyEntityOp(statecache.Op{
		Topic:     "entity",
		Key:       entityKey(n.Uuid, "roles.performer"),
		Tombstone: true,
	})
	if n.Encoder.Roles.Contains("performer") {
		t.Fatal("tombstone role: expected role removed")
	}
}

func TestApplyEntityOpPersonalMuteSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	listener := addTestNode(t, s, 1.0, 2.0)
	target := addTestNode(t, s, 1.0, 2.0)

	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(listener.Uuid, "mutes."+target.Uuid.String()),
		Value: []byte("true"),
	})
	if !listener.Encoder.Mutes.Contains(target.Uuid) {
		t.Fatal("set personal mute: expected target in Mutes")
	}

	s.applyEntityOp(statecache.Op{
		Topic:     "entity",
		Key:       entityKey(listener.Uuid, "mutes."+target.Uuid.String()),
		Tombstone: true,
	})
	if listener.Encoder.Mutes.Contains(target.Uuid) {
		t.Fatal("tombstone personal mute: expected target removed")
	}
}

func TestApplyEntityOpPersonalSoloSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	listener := addTestNode(t, s, 1.0, 2.0)
	target := addTestNode(t, s, 1.0, 2.0)

	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(listener.Uuid, "solos."+target.Uuid.String()),
		Value: []byte("true"),
	})
	if !listener.Encoder.Solos.Contains(target.Uuid) {
		t.Fatal("set personal solo: expected target in Solos")
	}

	s.applyEntityOp(statecache.Op{
		Topic:     "entity",
		Key:       entityKey(listener.Uuid, "solos."+target.Uuid.String()),
		Tombstone: true,
	})
	if listener.Encoder.Solos.Contains(target.Uuid) {
		t.Fatal("tombstone personal solo: expected target removed")
	}
}

func TestApplyEntityOpPersonalMuteRoleSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	listener := addTestNode(t, s, 1.0, 2.0)

	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(listener.Uuid, "mute-roles.performer"),
		Value: []byte("true"),
	})
	if !listener.Encoder.MuteRoles.Contains("performer") {
		t.Fatal("set mute-role: expected role in MuteRoles")
	}

	s.applyEntityOp(statecache.Op{
		Topic:     "entity",
		Key:       entityKey(listener.Uuid, "mute-roles.performer"),
		Tombstone: true,
	})
	if listener.Encoder.MuteRoles.Contains("performer") {
		t.Fatal("tombstone mute-role: expected role removed")
	}
}

// kicked & marker should be ignored at the mixer-level, no panic.
func TestApplyEntityOpKickedAndMarkerIgnored(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)

	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, ""), // marker
		Value: []byte("true"),
	})
	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "kicked"),
		Value: []byte("9999999999"),
	})
	// Both should be no-ops at mixer level.
	if n.Encoder.SpaceMuted {
		t.Fatal("marker/kicked must not flip SpaceMuted")
	}
}

// Unknown node: must not panic; mutator silently drops.
func TestApplyEntityOpMissingNode(t *testing.T) {
	s := newTestSpace()
	id := uuid.New()
	s.applyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(id, "gain"),
		Value: []byte("0.5"),
	})
	// no panic, no nodes added
	if len(s.Nodes) != 0 {
		t.Fatalf("expected no nodes; got %d", len(s.Nodes))
	}
}

// ─── space-side mutators ────────────────────────────────────────────────

func TestApplySpaceOpRoleMutedSetAndTombstone(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)
	n.Encoder.AddRole("performer")
	s.RefreshEncoderRoleEffects(n.Encoder)

	s.applySpaceOp(statecache.Op{
		Topic: "space",
		Key:   "roles-muted.performer",
		Value: []byte("true"),
	})
	if !n.Encoder.SpaceRoleMuted() {
		t.Fatal("set role-muted: expected encoder.spaceRoleMuted=true")
	}

	s.applySpaceOp(statecache.Op{
		Topic:     "space",
		Key:       "roles-muted.performer",
		Tombstone: true,
	})
	if n.Encoder.SpaceRoleMuted() {
		t.Fatal("tombstone role-muted: expected encoder.spaceRoleMuted=false")
	}
}

func TestApplySpaceOpRoleGainComposesAndClears(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 2.0, 2.0) // entity gain 2.0
	n.Encoder.AddRole("performer")
	s.RefreshEncoderRoleEffects(n.Encoder)

	s.applySpaceOp(statecache.Op{
		Topic: "space",
		Key:   "roles-gain.performer",
		Value: []byte("0.25"),
	})
	// effective gain = 2.0 × 0.25 = 0.5
	if got := n.Encoder.GainFactor(); got != gainFactor(0.5) {
		t.Fatalf("set role-gain: gainFactor=%v, want %v", got, gainFactor(0.5))
	}

	s.applySpaceOp(statecache.Op{
		Topic:     "space",
		Key:       "roles-gain.performer",
		Tombstone: true,
	})
	// back to entity gain 2.0
	if got := n.Encoder.GainFactor(); got != gainFactor(2.0) {
		t.Fatalf("tombstone role-gain: gainFactor=%v, want %v", got, gainFactor(2.0))
	}
}

func TestApplySpaceOpRoleAttenuationOverrides(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0) // entity exponent 1.0
	n.Encoder.AddRole("performer")
	s.RefreshEncoderRoleEffects(n.Encoder)

	s.applySpaceOp(statecache.Op{
		Topic: "space",
		Key:   "roles-attenuation.performer",
		Value: []byte("6.0"),
	})
	// override → exponent = 6/2 = 3.0
	if got := n.Encoder.PeerAttenuationExponent(); got != 3.0 {
		t.Fatalf("set role-att override: exponent=%v, want 3.0", got)
	}

	s.applySpaceOp(statecache.Op{
		Topic:     "space",
		Key:       "roles-attenuation.performer",
		Tombstone: true,
	})
	if got := n.Encoder.PeerAttenuationExponent(); got != 1.0 {
		t.Fatalf("tombstone role-att: exponent=%v, want 1.0 (entity 2/2)", got)
	}
}

// ─── role-membership ↔ space-state interaction ──────────────────────────

func TestNodeRoleAddPicksUpExistingRoleGain(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)

	// Space-wide role-gain set BEFORE the node has the role.
	s.setRoleGain("performer", 0.5)
	if got := n.Encoder.GainFactor(); got != gainFactor(1.0) {
		t.Fatalf("before role added: expected entity gain 1.0, got %v", got)
	}

	// Adding the role should pick up the existing role-gain.
	s.setNodeRole(n.Uuid, "performer")
	if got := n.Encoder.GainFactor(); got != gainFactor(0.5) {
		t.Fatalf("after role added: expected composed 0.5, got %v", got)
	}

	// Removing the role restores the entity gain.
	s.unsetNodeRole(n.Uuid, "performer")
	if got := n.Encoder.GainFactor(); got != gainFactor(1.0) {
		t.Fatalf("after role removed: expected entity gain 1.0, got %v", got)
	}
}

// ─── end-to-end: full Apply* + drain ─────────────────────────────────────

func TestApplyEntityOpEndToEndDrainAffectsState(t *testing.T) {
	s := newTestSpace()
	n := addTestNode(t, s, 1.0, 2.0)

	// Enqueue several ops without draining yet.
	s.ApplyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "muted"),
		Value: []byte("true"),
		OpID:  1,
	})
	s.ApplyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "gain"),
		Value: []byte("0.5"),
		OpID:  2,
	})
	s.ApplyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(n.Uuid, "roles.performer"),
		Value: []byte("true"),
		OpID:  3,
	})
	s.ApplySpaceOp(statecache.Op{
		Topic: "space",
		Key:   "roles-gain.performer",
		Value: []byte("0.5"),
		OpID:  4,
	})

	// Pre-drain: nothing applied yet.
	if n.Encoder.SpaceMuted {
		t.Fatal("pre-drain: SpaceMuted must still be false")
	}
	if got := n.Encoder.GainFactor(); got != gainFactor(1.0) {
		t.Fatalf("pre-drain: gain should still be 1.0, got %v", got)
	}

	s.processOpChanges()

	// Post-drain: all ops applied.
	if !n.Encoder.SpaceMuted {
		t.Fatal("post-drain: SpaceMuted should be true")
	}
	if !n.Encoder.Roles.Contains("performer") {
		t.Fatal("post-drain: role 'performer' should be set")
	}
	// Composed: entity gain 0.5 × role gain 0.5 = 0.25
	if got := n.Encoder.GainFactor(); got != gainFactor(0.25) {
		t.Fatalf("post-drain: composed gain factor=%v, want %v", got, gainFactor(0.25))
	}
}

// End-to-end with shouldIncludePeer outputs: a sequence of ops that ends
// with peer being soloed by listener should still include peer despite a
// stack of mutes — solo wins. This exercises the Phase 2 predicate via
// Phase 3 op-driven state.
//
// Solo / mute interaction is verified directly in core/ambisonic; here
// we only assert that the mutator path produces the same observable
// state (peer.SpaceMuted etc.) that the predicate keys on.
func TestApplyEntityOpEndToEndPredicateInputs(t *testing.T) {
	s := newTestSpace()
	listener := addTestNode(t, s, 1.0, 2.0)
	peer := addTestNode(t, s, 1.0, 2.0)

	// Stack mutes on peer.
	s.ApplyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(peer.Uuid, "muted"),
		Value: []byte("true"),
		OpID:  1,
	})
	s.ApplyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(peer.Uuid, "roles.performer"),
		Value: []byte("true"),
		OpID:  2,
	})
	s.ApplySpaceOp(statecache.Op{
		Topic: "space",
		Key:   "roles-muted.performer",
		Value: []byte("true"),
		OpID:  3,
	})
	s.ApplyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(listener.Uuid, "mute-roles.performer"),
		Value: []byte("true"),
		OpID:  4,
	})
	// And: listener solos peer.
	s.ApplyEntityOp(statecache.Op{
		Topic: "entity",
		Key:   entityKey(listener.Uuid, "solos."+peer.Uuid.String()),
		Value: []byte("true"),
		OpID:  5,
	})

	s.processOpChanges()

	// Verify the encoder state reflects all the ops:
	if !peer.Encoder.SpaceMuted {
		t.Error("peer.SpaceMuted should be true")
	}
	if !peer.Encoder.SpaceRoleMuted() {
		t.Error("peer.spaceRoleMuted should be true (role 'performer' is muted)")
	}
	if !listener.Encoder.MuteRoles.Contains("performer") {
		t.Error("listener.MuteRoles should contain 'performer'")
	}
	if !listener.Encoder.Solos.Contains(peer.Uuid) {
		t.Error("listener.Solos should contain peer")
	}
}
