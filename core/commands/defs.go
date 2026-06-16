package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Shared argument shapes — most commands take one of these. Keeping the
// types here (vs declaring inline in every Build) means a command's
// declaration is just two or three lines: validate + emit.

type entityIDArgs struct {
	EntityID uuid.UUID `json:"entity_id"`
}

type entityGainArgs struct {
	EntityID uuid.UUID `json:"entity_id"`
	Gain     float64   `json:"gain"`
}

type entityAttenuationArgs struct {
	EntityID    uuid.UUID `json:"entity_id"`
	Attenuation float64   `json:"attenuation"`
}

type entityKickArgs struct {
	EntityID uuid.UUID `json:"entity_id"`
	// Minutes from now until the kick expires. 0 means "forever" — the
	// resulting cache value is then 0; readers must interpret 0 as
	// no-expiry rather than "expired now".
	Mins int64 `json:"mins"`
}

type roleArgs struct {
	Role string `json:"role"`
}

type roleGainArgs struct {
	Role string  `json:"role"`
	Gain float64 `json:"gain"`
}

type roleAttenuationArgs struct {
	Role        string  `json:"role"`
	Attenuation float64 `json:"attenuation"`
}

type roleKickArgs struct {
	Role string `json:"role"`
	Mins int64  `json:"mins"`
}

// Tunable bounds for gain / attenuation. Match the existing checks in
// common.NodeConfigFromDirectTicket so values that round-trip through
// the cache stay within the same envelope as ticket-supplied defaults.
const (
	minGain        = 0.0
	maxGain        = 3.0
	minAttenuation = 0.0
	maxAttenuation = 3.0
)

func decodeArgs(name string, raw json.RawMessage, target any) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s: bad args: %w", name, err)
	}
	return nil
}

func requireEntity(name string, id uuid.UUID) error {
	if id == uuid.Nil {
		return fmt.Errorf("%s: entity_id required", name)
	}
	return nil
}

func requireRole(name string, role string) error {
	if role == "" {
		return fmt.Errorf("%s: role required", name)
	}
	return nil
}

func requireGain(name string, g float64) error {
	if g < minGain || g > maxGain {
		return fmt.Errorf("%s: gain %v out of [%v,%v]", name, g, minGain, maxGain)
	}
	return nil
}

func requireAttenuation(name string, a float64) error {
	if a < minAttenuation || a > maxAttenuation {
		return fmt.Errorf("%s: attenuation %v out of [%v,%v]", name, a, minAttenuation, maxAttenuation)
	}
	return nil
}

// kickExpiry converts a "minutes from now" argument into the cache
// value the kicked-keys hold. 0 ↔ no expiry (forever); otherwise the
// unix-millisecond deadline.
func kickExpiry(mins int64) (int64, error) {
	if mins < 0 {
		return 0, errors.New("mins must be >= 0 (0 = forever)")
	}
	if mins == 0 {
		return 0, nil
	}
	return time.Now().Add(time.Duration(mins) * time.Minute).UnixMilli(), nil
}

// All command specs. Order is informational only (mirrors
// command_types.md); the Registry indexes by Name.
var commandSpecs = []CommandSpec{
	// ─── space.entity ──────────────────────────────────────────────────
	{
		Name: "space.entity.mute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.entity.mute"
			var a entityIDArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "entity",
				Key:   a.EntityID.String() + ".muted",
				Value: true,
			}, nil
		},
	},
	{
		Name: "space.entity.unmute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.entity.unmute"
			var a entityIDArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "entity",
				Key:       a.EntityID.String() + ".muted",
				Tombstone: true,
			}, nil
		},
	},
	{
		Name: "space.entity.kick",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.entity.kick"
			var a entityKickArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			ttl, err := kickExpiry(a.Mins)
			if err != nil {
				return Op{}, fmt.Errorf("%s: %w", name, err)
			}
			return Op{
				Topic: "entity",
				Key:   a.EntityID.String() + ".kicked",
				Value: ttl,
			}, nil
		},
	},
	{
		Name: "space.entity.unkick",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.entity.unkick"
			var a entityIDArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "entity",
				Key:       a.EntityID.String() + ".kicked",
				Tombstone: true,
			}, nil
		},
	},
	{
		Name: "space.entity.set_gain",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.entity.set_gain"
			var a entityGainArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			if err := requireGain(name, a.Gain); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "entity",
				Key:   a.EntityID.String() + ".gain",
				Value: a.Gain,
			}, nil
		},
	},
	{
		Name: "space.entity.set_attenuation",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.entity.set_attenuation"
			var a entityAttenuationArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			if err := requireAttenuation(name, a.Attenuation); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "entity",
				Key:   a.EntityID.String() + ".attenuation",
				Value: a.Attenuation,
			}, nil
		},
	},

	// ─── space.role ────────────────────────────────────────────────────
	{
		Name: "space.role.mute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.mute"
			var a roleArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "space",
				Key:   "roles-muted." + a.Role,
				Value: true,
			}, nil
		},
	},
	{
		Name: "space.role.unmute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.unmute"
			var a roleArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "space",
				Key:       "roles-muted." + a.Role,
				Tombstone: true,
			}, nil
		},
	},
	{
		Name: "space.role.kick",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.kick"
			var a roleKickArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			ttl, err := kickExpiry(a.Mins)
			if err != nil {
				return Op{}, fmt.Errorf("%s: %w", name, err)
			}
			return Op{
				Topic: "space",
				Key:   "roles-kicked." + a.Role,
				Value: ttl,
			}, nil
		},
	},
	{
		Name: "space.role.unkick",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.unkick"
			var a roleArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "space",
				Key:       "roles-kicked." + a.Role,
				Tombstone: true,
			}, nil
		},
	},
	{
		Name: "space.role.set_gain",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.set_gain"
			var a roleGainArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			if err := requireGain(name, a.Gain); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "space",
				Key:   "roles-gain." + a.Role,
				Value: a.Gain,
			}, nil
		},
	},
	{
		Name: "space.role.unset_gain",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.unset_gain"
			var a roleArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "space",
				Key:       "roles-gain." + a.Role,
				Tombstone: true,
			}, nil
		},
	},
	{
		Name: "space.role.set_attenuation",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.set_attenuation"
			var a roleAttenuationArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			if err := requireAttenuation(name, a.Attenuation); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "space",
				Key:   "roles-attenuation." + a.Role,
				Value: a.Attenuation,
			}, nil
		},
	},
	{
		Name: "space.role.unset_attenuation",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "space.role.unset_attenuation"
			var a roleArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "space",
				Key:       "roles-attenuation." + a.Role,
				Tombstone: true,
			}, nil
		},
	},

	// ─── personal.entity ───────────────────────────────────────────────
	// my_id comes from the ticket — never an arg. Self-spoofing is
	// impossible because the client cannot influence the leading uuid
	// in the produced key.
	{
		Name: "personal.entity.mute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "personal.entity.mute"
			var a entityIDArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "entity",
				Key:   myUuid.String() + ".mutes." + a.EntityID.String(),
				Value: true,
			}, nil
		},
	},
	{
		Name: "personal.entity.unmute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "personal.entity.unmute"
			var a entityIDArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "entity",
				Key:       myUuid.String() + ".mutes." + a.EntityID.String(),
				Tombstone: true,
			}, nil
		},
	},
	{
		Name: "personal.entity.solo",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "personal.entity.solo"
			var a entityIDArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "entity",
				Key:   myUuid.String() + ".solos." + a.EntityID.String(),
				Value: true,
			}, nil
		},
	},
	{
		Name: "personal.entity.unsolo",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "personal.entity.unsolo"
			var a entityIDArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireEntity(name, a.EntityID); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "entity",
				Key:       myUuid.String() + ".solos." + a.EntityID.String(),
				Tombstone: true,
			}, nil
		},
	},

	// ─── personal.role ─────────────────────────────────────────────────
	{
		Name: "personal.role.mute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "personal.role.mute"
			var a roleArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			return Op{
				Topic: "entity",
				Key:   myUuid.String() + ".mute-roles." + a.Role,
				Value: true,
			}, nil
		},
	},
	{
		Name: "personal.role.unmute",
		Build: func(raw json.RawMessage, myUuid uuid.UUID) (Op, error) {
			const name = "personal.role.unmute"
			var a roleArgs
			if err := decodeArgs(name, raw, &a); err != nil {
				return Op{}, err
			}
			if err := requireRole(name, a.Role); err != nil {
				return Op{}, err
			}
			return Op{
				Topic:     "entity",
				Key:       myUuid.String() + ".mute-roles." + a.Role,
				Tombstone: true,
			}, nil
		},
	},
}

// DefaultRegistry returns a registry containing every command in
// commandSpecs. Failure is a programmer error (duplicate names) and
// panics so tests catch it immediately at package init time.
func DefaultRegistry() *Registry {
	r, err := NewRegistry(commandSpecs)
	if err != nil {
		panic(err)
	}
	return r
}

// DefaultRolePermissions is the hardcoded role → allowed-commands table
// used until per-space role configuration becomes a real thing. Lives
// next to the command catalog so adding a command surfaces the question
// "which roles can use it?" in the same diff.
//
// The reserved EveryoneRole entry's commands and read caps are granted
// to every holder regardless of which roles their ticket carries —
// including holders with no roles at all. Per-space role config (when
// it lands) follows the same shape: an optional "everyone" key
// alongside named roles.
var DefaultRolePermissions = map[string]RolePermissions{
	EveryoneRole: {
		Commands: []string{
			"personal.entity.mute", "personal.entity.unmute",
			"personal.role.mute", "personal.role.unmute",
		},
	},
	"admin": {
		Commands: []string{
			"space.entity.mute", "space.entity.unmute",
			"space.entity.kick", "space.entity.unkick",
			"space.entity.set_gain", "space.entity.set_attenuation",
			"space.role.mute", "space.role.unmute",
			"space.role.kick", "space.role.unkick",
			"space.role.set_gain", "space.role.unset_gain",
			"space.role.set_attenuation", "space.role.unset_attenuation",
			"personal.entity.solo", "personal.entity.unsolo",
		},
		Read: []string{ReadCapEntityAll, ReadCapSpaceRead},
	},
	"moderator": {
		Commands: []string{
			"space.entity.mute", "space.entity.unmute",
			"space.entity.kick", "space.entity.unkick",
			"personal.entity.solo", "personal.entity.unsolo",
		},
		Read: []string{ReadCapEntityAll, ReadCapSpaceRead},
	},
	"performer": {
		Commands: []string{
			"personal.entity.solo", "personal.entity.unsolo",
		},
	},
	// audience grants nothing beyond the everyone slot; kept as a
	// recognised role name so tickets carrying `audience` are explicit
	// metadata rather than a typo.
	"audience": {},
}

// DefaultAuthorizer returns an Authorizer backed by DefaultRolePermissions.
func DefaultAuthorizer() Authorizer {
	return NewMapAuthorizer(DefaultRolePermissions)
}
