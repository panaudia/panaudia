package space

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/ambisonic"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/statecache"
)

// ApplyEntityOp enqueues a cache op from the "entity" topic for the next
// drain. Safe to call from any goroutine. The op is interpreted later, on
// the audio thread, by processOpChanges().
//
// Convention: op.Value is the JSON-encoded value field only (e.g. `0.5`,
// `true`), NOT the wrapping `{"key":..., "value":...}` envelope. The Phase 4
// integration point in direct/backend.go::handleStringMessage extracts the
// inner value before constructing the Op.
func (space *BaseSpace) ApplyEntityOp(op statecache.Op) {
	space.enqueueOp(opKey{topic: "entity", key: op.Key}, op)
}

// ApplySpaceOp enqueues a cache op from the "space" topic. Same shape as
// ApplyEntityOp; same Value convention.
func (space *BaseSpace) ApplySpaceOp(op statecache.Op) {
	space.enqueueOp(opKey{topic: "space", key: op.Key}, op)
}

// enqueueOp dedups by OpID per (topic, key). >= matches Cache.Set
// last-write-wins semantics for the same-OpID edge case.
func (space *BaseSpace) enqueueOp(k opKey, op statecache.Op) {
	space.opChangesMu.Lock()
	defer space.opChangesMu.Unlock()
	if existing, ok := space.opChanges[k]; ok && op.OpID < existing.OpID {
		return
	}
	space.opChanges[k] = op
}

// processOpChanges drains the op queue with an atomic swap and dispatches
// each surviving op to applyEntityOp / applySpaceOp. Runs on the audio
// thread, ahead of processChanges() so any add/move/delete in the same
// tick sees op-derived state.
func (space *BaseSpace) processOpChanges() {
	space.opChangesMu.Lock()
	if len(space.opChanges) == 0 {
		space.opChangesMu.Unlock()
		return
	}
	drained := space.opChanges
	space.opChanges = make(map[opKey]statecache.Op)
	space.opChangesMu.Unlock()

	for k, op := range drained {
		switch k.topic {
		case "entity":
			space.applyEntityOp(op)
		case "space":
			space.applySpaceOp(op)
		}
	}
}

// splitEntityKey separates the leading uuid from the remaining dotted
// path of an entity flat key. Returns (uuid, "", true) for a bare
// `{uuid}` marker key, (uuid, "field", true) for `{uuid}.field`,
// (uuid, "field.x", true) for `{uuid}.field.x`, and (_, _, false) if
// the leading segment is not a valid uuid.
func splitEntityKey(key string) (uuid.UUID, string, bool) {
	dot := strings.IndexByte(key, '.')
	var head, rest string
	if dot < 0 {
		head = key
	} else {
		head = key[:dot]
		rest = key[dot+1:]
	}
	id, err := uuid.Parse(head)
	if err != nil {
		return uuid.UUID{}, "", false
	}
	return id, rest, true
}

// applyEntityOp dispatches a single entity-topic op to the per-leaf
// mutator based on the suffix after the leading uuid.
func (space *BaseSpace) applyEntityOp(op statecache.Op) {
	id, rest, ok := splitEntityKey(op.Key)
	if !ok {
		common.LogDebug("applyEntityOp: cannot parse uuid from key %q", op.Key)
		return
	}

	switch {
	case rest == "":
		// Existence marker — visibility/membership concern, handled by
		// MoqDataWriter.handleEntity (and equivalent on WebRTC). No mixer
		// state attached.
		return

	case rest == "gain":
		if op.Tombstone {
			space.unsetEntityGain(id)
		} else {
			var gain float64
			if !decodeOpValue(op, &gain) {
				common.LogWarn("applyEntityOp gain: parse failed for %q", op.Key)
				return
			}
			space.setEntityGain(id, gain)
		}

	case rest == "attenuation":
		if op.Tombstone {
			space.unsetEntityAttenuation(id)
		} else {
			var att float64
			if !decodeOpValue(op, &att) {
				common.LogWarn("applyEntityOp attenuation: parse failed for %q", op.Key)
				return
			}
			space.setEntityAttenuation(id, att)
		}

	case rest == "muted":
		if op.Tombstone {
			space.unsetEntityMuted(id)
		} else {
			space.setEntityMuted(id)
		}

	case rest == "kicked":
		// Connection-side concern (Phase 7 — KickGate / BouncerClient.kickFn).
		return

	case strings.HasPrefix(rest, "roles."):
		role := rest[len("roles."):]
		if role == "" {
			return
		}
		if op.Tombstone {
			space.unsetNodeRole(id, role)
		} else {
			space.setNodeRole(id, role)
		}

	case strings.HasPrefix(rest, "mutes."):
		other, err := uuid.Parse(rest[len("mutes."):])
		if err != nil {
			return
		}
		if op.Tombstone {
			space.unsetPersonalMute(id, other)
		} else {
			space.setPersonalMute(id, other)
		}

	case strings.HasPrefix(rest, "solos."):
		other, err := uuid.Parse(rest[len("solos."):])
		if err != nil {
			return
		}
		if op.Tombstone {
			space.unsetPersonalSolo(id, other)
		} else {
			space.setPersonalSolo(id, other)
		}

	case strings.HasPrefix(rest, "mute-roles."):
		role := rest[len("mute-roles."):]
		if role == "" {
			return
		}
		if op.Tombstone {
			space.unsetPersonalMuteRole(id, role)
		} else {
			space.setPersonalMuteRole(id, role)
		}

	case strings.HasPrefix(rest, "subspaces."):
		// Subspace membership is handled by the existing handleEntity tee
		// (it drives MoqDataWriter.nodeSubspaces / DataWriter visibility).
		// The audio-side subspace set on Encoder is populated at node-add
		// time from SpaceNodeConfig.SubSpaces; runtime updates here are not
		// part of the current command catalog.
		return
	}
}

// applySpaceOp dispatches a single space-topic op. Space keys have no
// uuid prefix; the leading segment names the role-rule field.
func (space *BaseSpace) applySpaceOp(op statecache.Op) {
	switch {
	case strings.HasPrefix(op.Key, "roles-muted."):
		role := op.Key[len("roles-muted."):]
		if role == "" {
			return
		}
		if op.Tombstone {
			space.unsetRoleMuted(role)
		} else {
			space.setRoleMuted(role)
		}

	case strings.HasPrefix(op.Key, "roles-gain."):
		role := op.Key[len("roles-gain."):]
		if role == "" {
			return
		}
		if op.Tombstone {
			space.unsetRoleGain(role)
		} else {
			var g float64
			if !decodeOpValue(op, &g) {
				common.LogWarn("applySpaceOp roles-gain: parse failed for %q", op.Key)
				return
			}
			space.setRoleGain(role, g)
		}

	case strings.HasPrefix(op.Key, "roles-attenuation."):
		role := op.Key[len("roles-attenuation."):]
		if role == "" {
			return
		}
		if op.Tombstone {
			space.unsetRoleAttenuation(role)
		} else {
			var a float64
			if !decodeOpValue(op, &a) {
				common.LogWarn("applySpaceOp roles-attenuation: parse failed for %q", op.Key)
				return
			}
			space.setRoleAttenuation(role, a)
		}

	case strings.HasPrefix(op.Key, "roles-kicked."):
		// Connection-side concern (Phase 7).
		return
	}
}

// decodeOpValue parses op.Value (JSON of the value field only) into target.
// Returns false on parse failure or empty value.
func decodeOpValue(op statecache.Op, target interface{}) bool {
	if len(op.Value) == 0 {
		return false
	}
	if err := json.Unmarshal(op.Value, target); err != nil {
		return false
	}
	return true
}

// RefreshEncoderRoleEffects recomputes an Encoder's cached role-derived
// state from the current BaseSpace role maps and the encoder's own Roles.
// Called from any mutator that changes either the encoder's role
// membership or one of the space-wide role maps.
//
// Composition rules (decided in plan/history/commands/server-enforcement-plan.md):
//   - role-mute is a veto: encoder.spaceRoleMuted = (encoder.Roles ∩ mutedRoles) ≠ ∅
//   - role-gain composes multiplicatively, multi-role conflict → min
//   - role-attenuation overrides (replaces), multi-role conflict → min
func (space *BaseSpace) RefreshEncoderRoleEffects(encoder *ambisonic.Encoder) {
	roles := encoder.Roles

	muted := false
	if !space.mutedRoles.IsEmpty() && !roles.IsEmpty() {
		muted = space.mutedRoles.ContainsAnyElement(roles)
	}
	encoder.SetSpaceRoleMuted(muted)

	multiplier := 1.0
	gainSet := false
	if len(space.roleGains) > 0 && !roles.IsEmpty() {
		for r := range roles.Iter() {
			if g, ok := space.roleGains[r]; ok {
				if !gainSet || g < multiplier {
					multiplier = g
					gainSet = true
				}
			}
		}
	}
	if !gainSet {
		multiplier = 1.0
	}
	encoder.SetRoleGainMultiplier(multiplier)

	var attOverride *float64
	if len(space.roleAttenuations) > 0 && !roles.IsEmpty() {
		for r := range roles.Iter() {
			if a, ok := space.roleAttenuations[r]; ok {
				if attOverride == nil || a < *attOverride {
					v := a
					attOverride = &v
				}
			}
		}
	}
	encoder.SetRoleAttenuationOverride(attOverride)
}

// refreshEncodersWithRole runs RefreshEncoderRoleEffects for every node
// that holds the given role. Used by space-wide mutators (role-mute /
// role-gain / role-attenuation) when their map state changes — only the
// encoders that actually carry that role can have their composed state
// changed.
func (space *BaseSpace) refreshEncodersWithRole(role string) {
	for _, node := range space.Nodes {
		if node.Encoder == nil {
			continue
		}
		if node.Encoder.Roles.Contains(role) {
			space.RefreshEncoderRoleEffects(node.Encoder)
		}
	}
}
