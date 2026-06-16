package direct

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/sessions"
	"github.com/panaudia/panaudia/core/space"
	"github.com/panaudia/panaudia/core/statecache"
)

// fakeSpace is a minimal ISpace: a node set plus the admission predicates.
// Everything else is a no-op.
type fakeSpace struct {
	mu    sync.Mutex
	nodes map[uuid.UUID]*space.Node
	full  bool
}

func newFakeSpace() *fakeSpace {
	return &fakeSpace{nodes: make(map[uuid.UUID]*space.Node)}
}

func (f *fakeSpace) GetNode(u uuid.UUID) (*space.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[u], nil
}

func (f *fakeSpace) SourceMaxReached() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.full
}

func (f *fakeSpace) AddNodeStyledWithId(u uuid.UUID, name string, position space.Position, config common.SpaceNodeConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[u] = &space.Node{}
}

func (f *fakeSpace) DeleteNode(u uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.nodes, u)
}

func (f *fakeSpace) AsJson() common.J { return common.J{} }
func (f *fakeSpace) UpdateNode(u uuid.UUID, position space.Position, rotation space.Rotation) error {
	return nil
}
func (f *fakeSpace) GetSessionIdForUuid(u uuid.UUID) (uint64, bool)             { return 0, false }
func (f *fakeSpace) OutputMaxReached() bool                                     { return false }
func (f *fakeSpace) EnsureSpaceSource(u uuid.UUID, isRaw, hasInput bool) uint64 { return 0 }
func (f *fakeSpace) EnableOut(u uuid.UUID)                                      {}
func (f *fakeSpace) SoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID)           {}
func (f *fakeSpace) UnsoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID)         {}
func (f *fakeSpace) AddSubspace(nodeId uuid.UUID, subspaceId uuid.UUID)         {}
func (f *fakeSpace) RemoveSubspace(node uuid.UUID, subspaceId uuid.UUID)        {}
func (f *fakeSpace) AddNodeQStyled(name string, position space.Position, style map[string]string, config common.SpaceNodeConfig) {
}
func (f *fakeSpace) IsCurrentlyCustering() bool     { return false }
func (f *fakeSpace) Self() *space.BaseSpace         { return nil }
func (f *fakeSpace) Process(doPartition bool) int   { return 0 }
func (f *fakeSpace) ApplyEntityOp(op statecache.Op) {}
func (f *fakeSpace) ApplySpaceOp(op statecache.Op)  {}

// noInputConfig returns a NodeConfig that avoids the jitter-buffer /
// output-encoder path (Input=false) so the test needs no audio plumbing.
func noInputConfig(id uuid.UUID, name string) common.NodeConfig {
	return common.NodeConfig{Uuid: id, Name: name,
		SpaceNodeConfig: common.SpaceNodeConfig{Input: false}}
}

// TestOrphanSpaceStateRejectedWithoutClobber: a connect for a uuid whose
// node is still in the space but has NO registry entry (orphaned state —
// the reconciler's territory) is rejected with SERVER_ERROR_DUPLICATE
// and must not install anything. This is the surviving piece of the
// findings §2.3 no-clobber guarantee; a LIVE duplicate is no longer
// rejected — it evicts (phase 5, Q4), covered in eviction_test.go.
func TestOrphanSpaceStateRejectedWithoutClobber(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	fs := newFakeSpace()
	backend.ISpace = fs

	id := uuid.UUID{42}
	// Node in the space, no registry entry, no maps: orphaned.
	fs.AddNodeStyledWithId(id, "ghost", space.Position{}, common.SpaceNodeConfig{})

	h, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "second"), nil, &sessions.FuncSession{}, "test")
	if h != nil {
		t.Fatal("admission over orphaned space state returned a handler")
	}
	if serr == nil || serr.Code != common.SERVER_ERROR_DUPLICATE {
		t.Fatalf("expected SERVER_ERROR_DUPLICATE, got %v", serr)
	}

	backend.Lock()
	defer backend.Unlock()
	if _, ok := backend.HandlersByUuid[id]; ok {
		t.Error("rejected admission installed a handler")
	}
	if _, ok := backend.BouncersByUuid[id]; ok {
		t.Error("rejected admission installed a bouncer")
	}
	if backend.Sessions.Get(id) != nil {
		t.Error("rejected admission left a registry entry")
	}
}

// TestAdmissionRejectedWhenFull: server-full is reported distinctly and
// installs nothing.
func TestAdmissionRejectedWhenFull(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	fs := newFakeSpace()
	fs.full = true
	backend.ISpace = fs

	id := uuid.UUID{43}
	h, serr := backend.NewConnectionHandlerWithError(noInputConfig(id, "x"), nil, &sessions.FuncSession{}, "test")
	if h != nil {
		t.Fatal("admission returned a handler on a full server")
	}
	if serr == nil || serr.Code != common.SERVER_ERROR_FULL {
		t.Fatalf("expected SERVER_ERROR_FULL, got %v", serr)
	}

	backend.Lock()
	defer backend.Unlock()
	if _, ok := backend.HandlersByUuid[id]; ok {
		t.Error("rejected admission installed a handler")
	}
	if _, ok := backend.BouncersByUuid[id]; ok {
		t.Error("rejected admission installed a bouncer")
	}
}

// TestNewConnectionHandlerNilInterface: the legacy interface method must
// return a clean nil interface on rejection (callers nil-check it).
func TestNewConnectionHandlerNilInterface(t *testing.T) {
	backend := newTestBackend()
	defer backend.Stop()
	fs := newFakeSpace()
	fs.full = true
	backend.ISpace = fs

	if h := backend.NewConnectionHandler(noInputConfig(uuid.UUID{44}, "x"), nil); h != nil {
		t.Fatalf("expected nil interface, got %v", h)
	}
}
