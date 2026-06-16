package commands

import (
	"strings"

	"github.com/google/uuid"
)

// KeyScope classifies a cached key by what its lifetime is tied to.
//
// Connection-scoped keys describe a live connection (presence, identity,
// personal preferences) and are tombstoned when their SUBJECT's
// connection departs — on every removal path. Policy-scoped keys are
// moderation/space rules; they are cleared only by explicit un-commands
// or TTL, never by any disconnect (neither the author's nor the
// subject's). See plan/history/state-cleanup/mechanism-design.md §1.
type KeyScope int

const (
	ScopeConnection KeyScope = iota
	ScopePolicy
)

// Policy-scoped fields on the "entity" topic — the fields the
// space.entity.* commands write ({target}.muted, {target}.kicked,
// {target}.gain, {target}.attenuation). This table MUST stay in sync
// with commandSpecs in defs.go: adding a command that writes a new
// moderation field means adding the field here, or the new field
// defaults to connection-scoped and gets swept on its subject's
// disconnect (over-clearing — safe but wrong for policy).
// scope_test.go walks commandSpecs to enforce the sync.
var entityPolicyFields = map[string]bool{
	"muted":       true,
	"kicked":      true,
	"gain":        true,
	"attenuation": true,
}

// ClassifyKey returns the scope and the subject uuid for a cached key on
// the given topic. The subject is the uuid the key is ABOUT (its leading
// uuid segment), independent of who wrote it; uuid.Nil means the key has
// no per-node subject (space-wide keys, unparseable keys).
//
// Current key inventory:
//
//	attributes  {uuid}.name / .ticket / .connection     → connection
//	entity      {uuid}                                   → connection (existence marker)
//	            {uuid}.subspaces.{id} / .roles.{role}    → connection (rebuilt from JWT on connect)
//	            {uuid}.mutes.{x} / .solos.{x} /
//	            .mute-roles.{role}                       → connection (personal prefs; subject = viewer)
//	            {uuid}.muted / .kicked / .gain /
//	            .attenuation                             → POLICY (moderation; subject = target)
//	space       roles-*.{role}                           → POLICY, no subject
//
// Unknown fields default to connection-scoped (over-clearing beats
// leaking; a new policy field must be added to entityPolicyFields).
func ClassifyKey(topic, key string) (KeyScope, uuid.UUID) {
	switch topic {
	case "space":
		return ScopePolicy, uuid.Nil
	case "entity":
		subject, field := splitSubjectKey(key)
		if entityPolicyFields[field] {
			return ScopePolicy, subject
		}
		return ScopeConnection, subject
	default:
		// "attributes" and any future cached topic with uuid-prefixed
		// keys: all connection-scoped.
		subject, _ := splitSubjectKey(key)
		return ScopeConnection, subject
	}
}

// splitSubjectKey splits "{uuid}.field.rest" into the subject uuid and
// the first field segment. A bare "{uuid}" yields an empty field; an
// unparseable prefix yields uuid.Nil.
func splitSubjectKey(key string) (uuid.UUID, string) {
	head, rest, _ := strings.Cut(key, ".")
	id, err := uuid.Parse(head)
	if err != nil {
		return uuid.Nil, ""
	}
	field, _, _ := strings.Cut(rest, ".")
	return id, field
}
