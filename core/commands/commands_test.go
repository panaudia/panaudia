package commands

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// rawArgs builds a json.RawMessage from a Go value — the wire form
// the dispatcher expects to receive.
func rawArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b
}

// allowAll is an Authorizer used by tests that want to focus on Build
// behaviour and not authorisation.
type allowAll struct{}

func (allowAll) IsCommandAllowed(roles []string, command string) bool { return true }
func (allowAll) HasReadCap(roles []string, cap string) bool           { return true }

// denyAll is its inverse.
type denyAll struct{}

func (denyAll) IsCommandAllowed(roles []string, command string) bool { return false }
func (denyAll) HasReadCap(roles []string, cap string) bool           { return false }

// TestRegistryRejectsBadSpecs locks down the registry's structural
// invariants — empty names, nil Build, duplicate names should all be
// caught at registry construction (i.e. at package init time when the
// real catalog is loaded).
func TestRegistryRejectsBadSpecs(t *testing.T) {
	t.Run("empty name", func(t *testing.T) {
		_, err := NewRegistry([]CommandSpec{{Name: "", Build: stubBuild}})
		if err == nil {
			t.Fatalf("expected error for empty name")
		}
	})
	t.Run("nil Build", func(t *testing.T) {
		_, err := NewRegistry([]CommandSpec{{Name: "x", Build: nil}})
		if err == nil {
			t.Fatalf("expected error for nil Build")
		}
	})
	t.Run("duplicate name", func(t *testing.T) {
		_, err := NewRegistry([]CommandSpec{
			{Name: "x", Build: stubBuild},
			{Name: "x", Build: stubBuild},
		})
		if err == nil {
			t.Fatalf("expected error for duplicate name")
		}
	})
}

func stubBuild(json.RawMessage, uuid.UUID) (Op, error) {
	return Op{Topic: "entity", Key: "stub", Value: true}, nil
}

// TestDefaultRegistryLoads asserts the production catalog passes the
// registry's validation rules (no duplicate names, no nil Build).
func TestDefaultRegistryLoads(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DefaultRegistry panicked: %v", r)
		}
	}()
	r := DefaultRegistry()
	if len(r.Names()) == 0 {
		t.Fatalf("expected at least one command in DefaultRegistry")
	}
}

// TestDispatchUnknownCommand checks the sentinel error path so callers
// (BouncerClient) can distinguish "client sent garbage" from "auth said
// no" if they want to log differently.
func TestDispatchUnknownCommand(t *testing.T) {
	reg := DefaultRegistry()
	_, err := Dispatch(reg, allowAll{}, "no.such.command", nil, uuid.New(), nil)
	if !errors.Is(err, ErrUnknownCommand) {
		t.Errorf("expected ErrUnknownCommand, got %v", err)
	}
}

// TestDispatchNotAuthorized verifies the auth gate runs before Build —
// a bad-args call from an unauthorised role must surface as
// ErrNotAuthorized, not ErrInvalidArguments. Otherwise an attacker can
// distinguish "I don't have this role" from "I have the role but my
// args were wrong".
func TestDispatchNotAuthorized(t *testing.T) {
	reg := DefaultRegistry()
	// Send valid args so we're sure the failure is from auth, not validation.
	args := rawArgs(t, entityIDArgs{EntityID: uuid.New()})
	_, err := Dispatch(reg, denyAll{}, "space.entity.mute", args, uuid.New(), nil)
	if !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("expected ErrNotAuthorized, got %v", err)
	}
}

// TestDispatchInvalidArguments — auth passes, args fail validation.
func TestDispatchInvalidArguments(t *testing.T) {
	reg := DefaultRegistry()
	// entity_id missing → Build returns an error → wrapped as
	// ErrInvalidArguments.
	args := rawArgs(t, struct{}{})
	_, err := Dispatch(reg, allowAll{}, "space.entity.mute", args, uuid.New(), nil)
	if !errors.Is(err, ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments, got %v", err)
	}
}

// ── Per-archetype Build coverage ────────────────────────────────────
//
// These tests are the catalog's behavioural contract: each archetype
// produces the exact key/value/topic/tombstone shape command_types.md
// describes. If any of these regress, the wire format has changed.

func TestSpaceEntityMuteUnmute(t *testing.T) {
	reg := DefaultRegistry()
	target := uuid.New()

	op, err := Dispatch(reg, allowAll{}, "space.entity.mute",
		rawArgs(t, entityIDArgs{EntityID: target}), uuid.New(), nil)
	if err != nil {
		t.Fatalf("mute dispatch: %v", err)
	}
	if op.Topic != "entity" || op.Key != target.String()+".muted" || op.Value != true || op.Tombstone {
		t.Errorf("mute op shape: %+v", op)
	}

	op, err = Dispatch(reg, allowAll{}, "space.entity.unmute",
		rawArgs(t, entityIDArgs{EntityID: target}), uuid.New(), nil)
	if err != nil {
		t.Fatalf("unmute dispatch: %v", err)
	}
	if op.Topic != "entity" || op.Key != target.String()+".muted" || !op.Tombstone {
		t.Errorf("unmute op shape: %+v", op)
	}
}

func TestSpaceEntitySetGain(t *testing.T) {
	reg := DefaultRegistry()
	target := uuid.New()

	op, err := Dispatch(reg, allowAll{}, "space.entity.set_gain",
		rawArgs(t, entityGainArgs{EntityID: target, Gain: 1.5}), uuid.New(), nil)
	if err != nil {
		t.Fatalf("set_gain dispatch: %v", err)
	}
	if op.Topic != "entity" || op.Key != target.String()+".gain" || op.Value != 1.5 {
		t.Errorf("set_gain op shape: %+v", op)
	}

	// Out-of-range gain must reject as invalid args.
	_, err = Dispatch(reg, allowAll{}, "space.entity.set_gain",
		rawArgs(t, entityGainArgs{EntityID: target, Gain: 99}), uuid.New(), nil)
	if !errors.Is(err, ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for out-of-range gain, got %v", err)
	}
}

func TestSpaceEntityKick(t *testing.T) {
	reg := DefaultRegistry()
	target := uuid.New()

	// mins=0 → forever (cache value 0).
	op, err := Dispatch(reg, allowAll{}, "space.entity.kick",
		rawArgs(t, entityKickArgs{EntityID: target, Mins: 0}), uuid.New(), nil)
	if err != nil {
		t.Fatalf("kick dispatch: %v", err)
	}
	if op.Topic != "entity" || op.Key != target.String()+".kicked" {
		t.Errorf("kick op shape: %+v", op)
	}
	if v, ok := op.Value.(int64); !ok || v != 0 {
		t.Errorf("kick forever should be int64 0, got %v (%T)", op.Value, op.Value)
	}

	// mins>0 → unix-millis deadline > now.
	op, err = Dispatch(reg, allowAll{}, "space.entity.kick",
		rawArgs(t, entityKickArgs{EntityID: target, Mins: 5}), uuid.New(), nil)
	if err != nil {
		t.Fatalf("kick dispatch: %v", err)
	}
	if v, ok := op.Value.(int64); !ok || v <= 0 {
		t.Errorf("kick with mins=5 should produce positive deadline, got %v (%T)", op.Value, op.Value)
	}

	// Negative mins is invalid.
	_, err = Dispatch(reg, allowAll{}, "space.entity.kick",
		rawArgs(t, entityKickArgs{EntityID: target, Mins: -1}), uuid.New(), nil)
	if !errors.Is(err, ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for negative mins, got %v", err)
	}
}

// TestPersonalCommandsUseMyUuidNotArgs — the key invariant of the
// explicit-command-name design: clients cannot influence the leading
// uuid in a personal.* op. The dispatcher always uses myUuid for that
// position.
func TestPersonalCommandsUseMyUuidNotArgs(t *testing.T) {
	reg := DefaultRegistry()
	myID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	target := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	cases := []struct {
		cmd     string
		wantTomb bool
		field    string
		argSeg   string
	}{
		{"personal.entity.mute", false, "mutes", target.String()},
		{"personal.entity.unmute", true, "mutes", target.String()},
		{"personal.entity.solo", false, "solos", target.String()},
		{"personal.entity.unsolo", true, "solos", target.String()},
	}
	for _, c := range cases {
		op, err := Dispatch(reg, allowAll{}, c.cmd,
			rawArgs(t, entityIDArgs{EntityID: target}), myID, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.cmd, err)
		}
		want := myID.String() + "." + c.field + "." + c.argSeg
		if op.Key != want {
			t.Errorf("%s: key = %q, want %q", c.cmd, op.Key, want)
		}
		if op.Tombstone != c.wantTomb {
			t.Errorf("%s: tombstone = %v, want %v", c.cmd, op.Tombstone, c.wantTomb)
		}
		if !strings.HasPrefix(op.Key, myID.String()+".") {
			t.Errorf("%s: key %q must start with myID", c.cmd, op.Key)
		}
	}

	// Personal role.* commands also key under myUuid.
	op, err := Dispatch(reg, allowAll{}, "personal.role.mute",
		rawArgs(t, roleArgs{Role: "performer"}), myID, nil)
	if err != nil {
		t.Fatalf("personal.role.mute: %v", err)
	}
	if op.Key != myID.String()+".mute-roles.performer" {
		t.Errorf("personal.role.mute key = %q", op.Key)
	}
}

func TestSpaceRoleCommandsTopicAndKeys(t *testing.T) {
	reg := DefaultRegistry()
	cases := []struct {
		cmd  string
		args any
		key  string
		tomb bool
	}{
		{"space.role.mute", roleArgs{Role: "audience"}, "roles-muted.audience", false},
		{"space.role.unmute", roleArgs{Role: "audience"}, "roles-muted.audience", true},
		{"space.role.set_gain", roleGainArgs{Role: "audience", Gain: 0.8}, "roles-gain.audience", false},
		{"space.role.unset_gain", roleArgs{Role: "audience"}, "roles-gain.audience", true},
		{"space.role.set_attenuation", roleAttenuationArgs{Role: "audience", Attenuation: 1.2}, "roles-attenuation.audience", false},
		{"space.role.unset_attenuation", roleArgs{Role: "audience"}, "roles-attenuation.audience", true},
	}
	for _, c := range cases {
		op, err := Dispatch(reg, allowAll{}, c.cmd, rawArgs(t, c.args), uuid.New(), nil)
		if err != nil {
			t.Fatalf("%s: %v", c.cmd, err)
		}
		if op.Topic != "space" {
			t.Errorf("%s: topic = %q, want \"space\"", c.cmd, op.Topic)
		}
		if op.Key != c.key {
			t.Errorf("%s: key = %q, want %q", c.cmd, op.Key, c.key)
		}
		if op.Tombstone != c.tomb {
			t.Errorf("%s: tombstone = %v, want %v", c.cmd, op.Tombstone, c.tomb)
		}
	}
}

func TestRoleArgsValidation(t *testing.T) {
	reg := DefaultRegistry()
	// Empty role string must fail validation, not produce
	// `roles-muted.` (which would cache under a colliding empty-suffix key).
	_, err := Dispatch(reg, allowAll{}, "space.role.mute",
		rawArgs(t, roleArgs{Role: ""}), uuid.New(), nil)
	if !errors.Is(err, ErrInvalidArguments) {
		t.Errorf("expected ErrInvalidArguments for empty role, got %v", err)
	}
}

// TestMapAuthorizerUnion checks the role-union rule: a holder with two
// roles can run the union of commands those roles allow.
func TestMapAuthorizerUnion(t *testing.T) {
	auth := NewMapAuthorizer(map[string]RolePermissions{
		"performer": {Commands: []string{"personal.entity.mute"}},
		"moderator": {Commands: []string{"space.entity.mute"}},
	})
	// Performer alone cannot do space.entity.mute.
	if auth.IsCommandAllowed([]string{"performer"}, "space.entity.mute") {
		t.Errorf("performer should not be allowed space.entity.mute")
	}
	// Moderator alone cannot do personal.entity.mute.
	if auth.IsCommandAllowed([]string{"moderator"}, "personal.entity.mute") {
		t.Errorf("moderator should not be allowed personal.entity.mute")
	}
	// Holding both roles unlocks both commands.
	if !auth.IsCommandAllowed([]string{"performer", "moderator"}, "space.entity.mute") {
		t.Errorf("combined roles should grant space.entity.mute")
	}
	if !auth.IsCommandAllowed([]string{"performer", "moderator"}, "personal.entity.mute") {
		t.Errorf("combined roles should grant personal.entity.mute")
	}
	// Unknown roles contribute nothing.
	if auth.IsCommandAllowed([]string{"ghost"}, "space.entity.mute") {
		t.Errorf("unknown role should not grant any command")
	}
}

// TestMapAuthorizerEveryoneRole locks down the reserved EveryoneRole
// rule: its commands and read caps are granted to every holder,
// including holders carrying no roles at all. A specific role still
// composes on top, and commands not in either everyone or the held
// roles are still denied.
func TestMapAuthorizerEveryoneRole(t *testing.T) {
	auth := NewMapAuthorizer(map[string]RolePermissions{
		EveryoneRole: {
			Commands: []string{"personal.entity.mute"},
			Read:     []string{ReadCapEntityAll},
		},
		"moderator": {Commands: []string{"space.entity.mute"}},
	})
	// No roles at all → still gets the everyone command + read cap.
	if !auth.IsCommandAllowed(nil, "personal.entity.mute") {
		t.Errorf("everyone command should be allowed with no roles")
	}
	if !auth.HasReadCap(nil, ReadCapEntityAll) {
		t.Errorf("everyone read cap should be granted with no roles")
	}
	// Empty-slice roles list behaves identically to nil.
	if !auth.IsCommandAllowed([]string{}, "personal.entity.mute") {
		t.Errorf("everyone command should be allowed with empty roles slice")
	}
	// Specific role composes on top.
	if !auth.IsCommandAllowed([]string{"moderator"}, "personal.entity.mute") {
		t.Errorf("everyone command should also be granted to moderator")
	}
	if !auth.IsCommandAllowed([]string{"moderator"}, "space.entity.mute") {
		t.Errorf("moderator should still get their own commands")
	}
	// Commands not in everyone or any held role are still denied.
	if auth.IsCommandAllowed(nil, "space.entity.mute") {
		t.Errorf("non-everyone command should be denied with no roles")
	}
	// Read caps not granted by everyone or any held role are still denied.
	if auth.HasReadCap(nil, ReadCapSpaceRead) {
		t.Errorf("non-everyone read cap should be denied with no roles")
	}
}

// TestDefaultRolePermissionsEveryoneEntry locks down what the catalog
// actually publishes via the everyone slot — the four personal-mute
// commands that every holder may invoke regardless of ticket roles.
func TestDefaultRolePermissionsEveryoneEntry(t *testing.T) {
	perms, ok := DefaultRolePermissions[EveryoneRole]
	if !ok {
		t.Fatalf("DefaultRolePermissions missing %q entry", EveryoneRole)
	}
	want := map[string]bool{
		"personal.entity.mute":   true,
		"personal.entity.unmute": true,
		"personal.role.mute":     true,
		"personal.role.unmute":   true,
	}
	got := make(map[string]bool, len(perms.Commands))
	for _, c := range perms.Commands {
		got[c] = true
	}
	for cmd := range want {
		if !got[cmd] {
			t.Errorf("everyone entry missing command %q", cmd)
		}
	}
	for cmd := range got {
		if !want[cmd] {
			t.Errorf("everyone entry has unexpected command %q", cmd)
		}
	}
	if len(perms.Read) != 0 {
		t.Errorf("everyone entry should grant no read caps by default, got %v", perms.Read)
	}
}

// TestDefaultRolePermissionsReferenceRealCommands prevents accidental
// drift where DefaultRolePermissions lists a command that no spec
// defines (typo, removed command, etc.).
func TestDefaultRolePermissionsReferenceRealCommands(t *testing.T) {
	reg := DefaultRegistry()
	for role, perms := range DefaultRolePermissions {
		for _, cmd := range perms.Commands {
			if _, ok := reg.Get(cmd); !ok {
				t.Errorf("role %q references unknown command %q", role, cmd)
			}
		}
	}
}

// TestDefaultRolePermissionsReadCapsAreKnown locks down the read-cap
// side of the same drift check: anything in DefaultRolePermissions[*]
// .Read must be a name listed in AllReadCaps. New caps need to be
// added to both AllReadCaps and the JSON-schema enum together.
func TestDefaultRolePermissionsReadCapsAreKnown(t *testing.T) {
	known := make(map[string]bool, len(AllReadCaps))
	for _, c := range AllReadCaps {
		known[c] = true
	}
	for role, perms := range DefaultRolePermissions {
		for _, c := range perms.Read {
			if !known[c] {
				t.Errorf("role %q references unknown read cap %q", role, c)
			}
		}
	}
}
