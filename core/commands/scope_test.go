package commands

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// TestClassifyKeyDirect covers the non-command key shapes: server-written
// entity records, self-published attributes, and degenerate keys.
func TestClassifyKeyDirect(t *testing.T) {
	subject := uuid.New()
	s := subject.String()

	cases := []struct {
		topic, key  string
		wantScope   KeyScope
		wantSubject uuid.UUID
	}{
		// attributes — all connection-scoped
		{"attributes", s + ".name", ScopeConnection, subject},
		{"attributes", s + ".ticket", ScopeConnection, subject},
		{"attributes", s + ".ticket.colour", ScopeConnection, subject},
		{"attributes", s + ".connection", ScopeConnection, subject},
		{"attributes", s + ".some-future-field", ScopeConnection, subject},

		// entity — server-written connection records
		{"entity", s, ScopeConnection, subject}, // existence marker
		{"entity", s + ".subspaces." + uuid.Nil.String(), ScopeConnection, subject},
		{"entity", s + ".roles.admin", ScopeConnection, subject},

		// entity — moderation fields are policy-scoped
		{"entity", s + ".muted", ScopePolicy, subject},
		{"entity", s + ".kicked", ScopePolicy, subject},
		{"entity", s + ".gain", ScopePolicy, subject},
		{"entity", s + ".attenuation", ScopePolicy, subject},

		// entity — unknown field defaults to connection-scoped
		{"entity", s + ".mystery", ScopeConnection, subject},

		// space — all policy, no subject
		{"space", "roles-muted.admin", ScopePolicy, uuid.Nil},
		{"space", "roles-kicked.performer", ScopePolicy, uuid.Nil},
		{"space", "roles-gain.admin", ScopePolicy, uuid.Nil},
		{"space", "roles-attenuation.admin", ScopePolicy, uuid.Nil},

		// degenerate keys: no parseable subject
		{"attributes", "node-A.name", ScopeConnection, uuid.Nil},
		{"entity", "not-a-uuid", ScopeConnection, uuid.Nil},
		{"attributes", "", ScopeConnection, uuid.Nil},
	}

	for _, c := range cases {
		scope, subj := ClassifyKey(c.topic, c.key)
		if scope != c.wantScope || subj != c.wantSubject {
			t.Errorf("ClassifyKey(%q, %q) = (%v, %s), want (%v, %s)",
				c.topic, c.key, scope, subj, c.wantScope, c.wantSubject)
		}
	}
}

// TestClassifyKeyCoversCommandCatalog walks every command in
// commandSpecs, builds its op with sample args, and checks the produced
// (topic, key) classifies as expected. A command missing from the
// expectation table fails the test — this is the sync guard between
// defs.go and scope.go: adding a command forces the question "what scope
// is the field it writes?".
func TestClassifyKeyCoversCommandCatalog(t *testing.T) {
	myUuid := uuid.New()
	target := uuid.New()

	type expect struct {
		scope   KeyScope
		subject uuid.UUID // uuid.Nil = no subject
	}

	// Expected classification per command name. subject semantics:
	// space.entity.* are ABOUT the target; personal.* are ABOUT the
	// issuing viewer; space.role.* are about the space.
	expectations := map[string]expect{
		"space.entity.mute":            {ScopePolicy, target},
		"space.entity.unmute":          {ScopePolicy, target},
		"space.entity.kick":            {ScopePolicy, target},
		"space.entity.unkick":          {ScopePolicy, target},
		"space.entity.set_gain":        {ScopePolicy, target},
		"space.entity.set_attenuation": {ScopePolicy, target},

		"space.role.mute":              {ScopePolicy, uuid.Nil},
		"space.role.unmute":            {ScopePolicy, uuid.Nil},
		"space.role.kick":              {ScopePolicy, uuid.Nil},
		"space.role.unkick":            {ScopePolicy, uuid.Nil},
		"space.role.set_gain":          {ScopePolicy, uuid.Nil},
		"space.role.unset_gain":        {ScopePolicy, uuid.Nil},
		"space.role.set_attenuation":   {ScopePolicy, uuid.Nil},
		"space.role.unset_attenuation": {ScopePolicy, uuid.Nil},

		"personal.entity.mute":   {ScopeConnection, myUuid},
		"personal.entity.unmute": {ScopeConnection, myUuid},
		"personal.entity.solo":   {ScopeConnection, myUuid},
		"personal.entity.unsolo": {ScopeConnection, myUuid},
		"personal.role.mute":     {ScopeConnection, myUuid},
		"personal.role.unmute":   {ScopeConnection, myUuid},
	}

	// Sample args sufficient for every command's validator.
	args := json.RawMessage(fmt.Sprintf(
		`{"entity_id":%q,"role":"admin","gain":1.0,"attenuation":1.0,"mins":5}`,
		target))

	for _, spec := range commandSpecs {
		want, ok := expectations[spec.Name]
		if !ok {
			t.Errorf("command %q has no scope expectation — classify its key in scope.go and add it here", spec.Name)
			continue
		}
		op, err := spec.Build(args, myUuid)
		if err != nil {
			t.Errorf("command %q: Build failed with sample args: %v", spec.Name, err)
			continue
		}
		scope, subject := ClassifyKey(op.Topic, op.Key)
		if scope != want.scope || subject != want.subject {
			t.Errorf("command %q writes (%q, %q) → classified (%v, %s), want (%v, %s)",
				spec.Name, op.Topic, op.Key, scope, subject, want.scope, want.subject)
		}
	}

	// Inverse guard: expectations for commands that no longer exist.
	known := make(map[string]bool, len(commandSpecs))
	for _, spec := range commandSpecs {
		known[spec.Name] = true
	}
	for name := range expectations {
		if !known[name] {
			t.Errorf("expectation for unknown command %q — remove it", name)
		}
	}
}
