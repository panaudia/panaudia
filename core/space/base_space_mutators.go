package space

import (
	"github.com/google/uuid"
)

// Per-leaf mutators that turn a parsed cache op into a concrete mutation
// on Encoder / BaseSpace state. Called from applyEntityOp / applySpaceOp
// on the audio thread.
//
// All mutators are no-ops when the target node is unknown — ops can
// arrive ahead of an entity's add (race during connection setup), and
// the cache replays them after; an authoritative `processOpChanges`
// drain runs every tick so missing nodes simply skip until they exist.

// ─── entity scalar fields ────────────────────────────────────────────

// setEntityGain replaces this entity's configured gain. The cached
// roleGainMultiplier is preserved across the change, so any active
// role-gain stays composed in.
func (space *BaseSpace) setEntityGain(id uuid.UUID, gain float64) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.SetGain(gain)
}

// unsetEntityGain restores this entity's gain to its node-config default
// (the gain it was constructed with). roleGainMultiplier is preserved.
func (space *BaseSpace) unsetEntityGain(id uuid.UUID) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.SetGain(node.config.Gain)
}

func (space *BaseSpace) setEntityAttenuation(id uuid.UUID, att float64) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.SetAttenuation(att)
}

func (space *BaseSpace) unsetEntityAttenuation(id uuid.UUID) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.SetAttenuation(node.config.Attenuation)
}

func (space *BaseSpace) setEntityMuted(id uuid.UUID) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.SetSpaceMuted(true)
}

func (space *BaseSpace) unsetEntityMuted(id uuid.UUID) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.SetSpaceMuted(false)
}

// ─── entity role membership ──────────────────────────────────────────

// setNodeRole adds a role to the node's role set and refreshes its
// composed role-effects (the new role may bring a role-gain, role-att,
// or role-mute that was already in space-wide state).
func (space *BaseSpace) setNodeRole(id uuid.UUID, role string) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.AddRole(role)
	space.RefreshEncoderRoleEffects(node.Encoder)
}

func (space *BaseSpace) unsetNodeRole(id uuid.UUID, role string) {
	node, ok := space.Nodes[id]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.RemoveRole(role)
	space.RefreshEncoderRoleEffects(node.Encoder)
}

// ─── personal (listener-side) mutes / solos / mute-roles ─────────────

func (space *BaseSpace) setPersonalMute(listenerID, otherID uuid.UUID) {
	node, ok := space.Nodes[listenerID]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.AddMute(otherID)
}

func (space *BaseSpace) unsetPersonalMute(listenerID, otherID uuid.UUID) {
	node, ok := space.Nodes[listenerID]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.RemoveMute(otherID)
}

func (space *BaseSpace) setPersonalSolo(listenerID, otherID uuid.UUID) {
	node, ok := space.Nodes[listenerID]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.AddSolo(otherID)
}

func (space *BaseSpace) unsetPersonalSolo(listenerID, otherID uuid.UUID) {
	node, ok := space.Nodes[listenerID]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.RemoveSolo(otherID)
}

func (space *BaseSpace) setPersonalMuteRole(listenerID uuid.UUID, role string) {
	node, ok := space.Nodes[listenerID]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.AddMuteRole(role)
}

func (space *BaseSpace) unsetPersonalMuteRole(listenerID uuid.UUID, role string) {
	node, ok := space.Nodes[listenerID]
	if !ok || node.Encoder == nil {
		return
	}
	node.Encoder.RemoveMuteRole(role)
}

// ─── space-wide role rules ───────────────────────────────────────────

func (space *BaseSpace) setRoleMuted(role string) {
	if space.mutedRoles.Contains(role) {
		return
	}
	space.mutedRoles.Add(role)
	space.refreshEncodersWithRole(role)
}

func (space *BaseSpace) unsetRoleMuted(role string) {
	if !space.mutedRoles.Contains(role) {
		return
	}
	space.mutedRoles.Remove(role)
	space.refreshEncodersWithRole(role)
}

func (space *BaseSpace) setRoleGain(role string, gain float64) {
	if existing, ok := space.roleGains[role]; ok && existing == gain {
		return
	}
	space.roleGains[role] = gain
	space.refreshEncodersWithRole(role)
}

func (space *BaseSpace) unsetRoleGain(role string) {
	if _, ok := space.roleGains[role]; !ok {
		return
	}
	delete(space.roleGains, role)
	space.refreshEncodersWithRole(role)
}

func (space *BaseSpace) setRoleAttenuation(role string, att float64) {
	if existing, ok := space.roleAttenuations[role]; ok && existing == att {
		return
	}
	space.roleAttenuations[role] = att
	space.refreshEncodersWithRole(role)
}

func (space *BaseSpace) unsetRoleAttenuation(role string) {
	if _, ok := space.roleAttenuations[role]; !ok {
		return
	}
	delete(space.roleAttenuations, role)
	space.refreshEncodersWithRole(role)
}

