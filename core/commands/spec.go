// Package commands defines the catalog of named control commands that
// authenticated clients can invoke, plus the pure dispatch + role-based
// authorisation logic that turns each command into a single cache op.
//
// The package has no transport or backend dependencies. The full flow
// across the server is:
//
//	control track / data channel  →  ControlMessage{type:"command", message:{command, args}}
//	                              →  BouncerClient.HandleCommand(name, args)
//	                              →  commands.Dispatch(...)
//	                              →  Op{Topic, Key, Value, Tombstone}
//	                              →  bouncer.SendString(topic, encodedOp)  (existing cache path)
//
// Adding a command means adding one entry to defs.go — the spec there
// is the single source of truth for the command name, its argument
// shape, validation, and the resulting op.
package commands

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Op is the result of invoking a command — exactly one cache operation.
// The op is then encoded by the caller (using statecache.BuildOp /
// BuildTombstoneOp) and pushed through the existing bouncer write path,
// so commands flow into the same cache → broadcast pipeline as the
// server-internal entity/attribute writes.
type Op struct {
	Topic     string
	Key       string
	Value     any  // ignored when Tombstone is true
	Tombstone bool
}

// CommandSpec is the declarative entry for a single named command.
// Build owns the entire decode → validate → emit pipeline; it returns
// the Op (whose Topic field also tells the dispatcher where to route
// it) so the topic isn't duplicated on the spec itself.
type CommandSpec struct {
	Name  string
	Build func(args json.RawMessage, myUuid uuid.UUID) (Op, error)
}

// RolePermissions is the per-role configuration: what write commands
// the holder may issue, and what read scopes the holder gains. Mirrors
// the JSON-schema shape in plan/history/commands/roles-schema.json. Both fields
// default to empty when omitted.
type RolePermissions struct {
	Commands []string `json:"commands,omitempty"`
	Read     []string `json:"read,omitempty"`
}

// Read scope names. Held as constants so callers don't typo and the
// schema-vs-code mapping stays explicit.
const (
	// ReadCapEntityAll grants visibility into every node's entity ops,
	// not just keys prefixed with the holder's own uuid (which is the
	// default per-client filter applied by MoqDataWriter / DataWriter).
	ReadCapEntityAll = "entity.all"

	// ReadCapSpaceRead grants delivery of the "space" topic — the
	// space-wide role-rule record (roles-muted, roles-kicked,
	// roles-gain, roles-attenuation) written by the eight
	// `space.role.*` commands. Without it, the per-connection space
	// output channel exists but is never written to (server gates at
	// the writer's sendSpace path). See
	// plan/history/commands/space-read-path-plan.md.
	ReadCapSpaceRead = "space.read"
)

// EveryoneRole is a reserved key in the role-permissions table whose
// commands and read caps are granted to every holder regardless of
// which roles their ticket carries — including holders with no roles
// at all. Per-space role configuration should treat this name as
// reserved (it is not a role a ticket can be assigned, only a slot in
// the permissions map).
const EveryoneRole = "everyone"

// AllReadCaps lists every defined read scope. ResolveReadCaps iterates
// this when flattening role grants. Add new caps here when the schema
// grows.
var AllReadCaps = []string{
	ReadCapEntityAll,
	ReadCapSpaceRead,
}

// Authorizer answers two questions for a holder identified by a set of
// role names:
//   - may they invoke this named command?  (write side)
//   - do they hold this named read scope?  (read side)
//
// The Space (or any caller) provides one. The default implementation
// in this package is MapAuthorizer.
type Authorizer interface {
	IsCommandAllowed(roles []string, command string) bool
	HasReadCap(roles []string, cap string) bool
}

// MapAuthorizer is the obvious implementation: a static role-name →
// RolePermissions table. Lookup is the union across all the holder's
// roles, matching the rule from command_types.md.
type MapAuthorizer struct {
	permissions map[string]RolePermissions
}

// NewMapAuthorizer builds a MapAuthorizer from a role → RolePermissions
// map. The map is referenced directly (not copied) — callers should not
// mutate it after construction.
func NewMapAuthorizer(permissions map[string]RolePermissions) *MapAuthorizer {
	return &MapAuthorizer{permissions: permissions}
}

// IsCommandAllowed returns true if any of the holder's roles grants the
// named command, or if the reserved EveryoneRole entry grants it (in
// which case the holder's roles list is irrelevant).
func (a *MapAuthorizer) IsCommandAllowed(roles []string, command string) bool {
	if a.roleGrantsCommand(EveryoneRole, command) {
		return true
	}
	for _, r := range roles {
		if a.roleGrantsCommand(r, command) {
			return true
		}
	}
	return false
}

// HasReadCap returns true if any of the holder's roles grants the named
// read scope, or if the reserved EveryoneRole entry grants it.
func (a *MapAuthorizer) HasReadCap(roles []string, cap string) bool {
	if a.roleGrantsReadCap(EveryoneRole, cap) {
		return true
	}
	for _, r := range roles {
		if a.roleGrantsReadCap(r, cap) {
			return true
		}
	}
	return false
}

func (a *MapAuthorizer) roleGrantsCommand(role, command string) bool {
	perms, ok := a.permissions[role]
	if !ok {
		return false
	}
	for _, name := range perms.Commands {
		if name == command {
			return true
		}
	}
	return false
}

func (a *MapAuthorizer) roleGrantsReadCap(role, cap string) bool {
	perms, ok := a.permissions[role]
	if !ok {
		return false
	}
	for _, c := range perms.Read {
		if c == cap {
			return true
		}
	}
	return false
}

// ResolveReadCaps flattens the union of read scopes across the given
// roles into a set, keyed by cap name. Called once at authentication
// time so the per-envelope path doesn't have to walk the roles list.
// Works on any Authorizer — iterates AllReadCaps and asks the
// authorizer for each.
func ResolveReadCaps(a Authorizer, roles []string) map[string]bool {
	out := make(map[string]bool, len(AllReadCaps))
	for _, cap := range AllReadCaps {
		if a.HasReadCap(roles, cap) {
			out[cap] = true
		}
	}
	return out
}

// Registry is a name-keyed lookup of CommandSpecs.
type Registry struct {
	specs map[string]CommandSpec
}

// NewRegistry builds a Registry from a slice of specs. Returns an error
// if any name collides — the catalog should have one entry per name.
func NewRegistry(specs []CommandSpec) (*Registry, error) {
	r := &Registry{specs: make(map[string]CommandSpec, len(specs))}
	for _, s := range specs {
		if s.Name == "" {
			return nil, errors.New("commands: spec with empty name")
		}
		if s.Build == nil {
			return nil, fmt.Errorf("commands: spec %q has nil Build", s.Name)
		}
		if _, dup := r.specs[s.Name]; dup {
			return nil, fmt.Errorf("commands: duplicate spec name %q", s.Name)
		}
		r.specs[s.Name] = s
	}
	return r, nil
}

// Get looks up a spec by name.
func (r *Registry) Get(name string) (CommandSpec, bool) {
	s, ok := r.specs[name]
	return s, ok
}

// Names returns every registered command name. Useful for debug /
// introspection and for building default role permissions.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.specs))
	for name := range r.specs {
		out = append(out, name)
	}
	return out
}

// Sentinel errors returned by Dispatch. Callers (notably BouncerClient)
// log these at debug level and silently drop the command — there is no
// error feedback channel to the client by design.
var (
	ErrUnknownCommand   = errors.New("commands: unknown command")
	ErrNotAuthorized    = errors.New("commands: not authorized")
	ErrInvalidArguments = errors.New("commands: invalid arguments")
)

// Dispatch is the pure entry point: look up the command, check
// authorisation against the caller's roles, then build the resulting
// op. No I/O — the caller (BouncerClient) is responsible for encoding
// and forwarding the op into the cache write path.
func Dispatch(
	reg *Registry,
	auth Authorizer,
	cmdName string,
	args json.RawMessage,
	myUuid uuid.UUID,
	myRoles []string,
) (Op, error) {
	spec, ok := reg.Get(cmdName)
	if !ok {
		return Op{}, fmt.Errorf("%w: %s", ErrUnknownCommand, cmdName)
	}
	if auth == nil || !auth.IsCommandAllowed(myRoles, cmdName) {
		return Op{}, fmt.Errorf("%w: %s", ErrNotAuthorized, cmdName)
	}
	op, err := spec.Build(args, myUuid)
	if err != nil {
		return Op{}, fmt.Errorf("%w: %s: %v", ErrInvalidArguments, cmdName, err)
	}
	return op, nil
}
