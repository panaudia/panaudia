package space

import (
	"fmt"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/ambisonic"
	"github.com/panaudia/panaudia/core/buffers"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
	"github.com/panaudia/panaudia/core/statecache"
	//"github.com/panaudia/panaudia/core/mix"
	"sync"
)

type ISpace interface {
	AsJson() common.J
	GetNode(Uuid uuid.UUID) (*Node, error)
	UpdateNode(Uuid uuid.UUID, position Position, rotation Rotation) error
	GetSessionIdForUuid(Uuid uuid.UUID) (uint64, bool)
	SourceMaxReached() bool
	OutputMaxReached() bool
	AddNodeStyledWithId(Uuid uuid.UUID, name string, position Position, config common.SpaceNodeConfig)
	EnsureSpaceSource(Uuid uuid.UUID, isRaw bool, hasInput bool) uint64
	EnableOut(Uuid uuid.UUID)
	DeleteNode(Uuid uuid.UUID)
	SoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID)
	UnsoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID)
	AddSubspace(nodeId uuid.UUID, subspaceId uuid.UUID)
	RemoveSubspace(node uuid.UUID, subspaceId uuid.UUID)
	AddNodeQStyled(name string, position Position, style map[string]string, config common.SpaceNodeConfig)
	IsCurrentlyCustering() bool
	Self() *BaseSpace
	Process(doPartition bool) int
	ApplyEntityOp(op statecache.Op)
	ApplySpaceOp(op statecache.Op)
}

type ISourceManager interface {
	EnsureSource(Uuid uuid.UUID,
		sourceDelegate common.UdpNodeIODelegate,
		statsDelegate buffers.BufferStatsDelegate,
		isRaw bool,
		hasInput bool) uint64
	GetInput(Uuid uuid.UUID) inout.MonoInput
	GetOutput(Uuid uuid.UUID) inout.AmbisonicOutput
	GetSessionIdForUuid(Uuid uuid.UUID) (uint64, bool)
	SetRotationForUuid(rotation common.Rotation, Uuid uuid.UUID)
	FreeSource(Uuid uuid.UUID)
}

type IMessageSender interface {
	SendString(topic string, msg string)
	SendData(topic string, data []byte)
}

type IMessageReceiver interface {
	SetReceiveSender(receiveSender IMessageSender)
}

type IMessageSenderReceiver interface {
	IMessageSender
	IMessageReceiver
}

type BaseSpace struct {
	Name            string
	AmbisonicOrder  int
	ChannelCount    int
	NodeMixCount    int
	MaxSources      int
	Size            float64
	Nodes           map[uuid.UUID]*Node
	AllNodeEncoders []*ambisonic.Encoder
	OpenToChange    bool
	changesQueue    chan NodeChange
	SourceManager   ISourceManager
	DoneCh          chan int
	Wg              *sync.WaitGroup
	Renderer        IRenderer
	GlobalBuffer    []float32
	GlobalCount     int
	OutputCount     int
	ticker          int64
	StatsDelegate   buffers.BufferStatsDelegate
	ForceGlobals    bool
	UsingGroups     bool
	TimeoutTicks    int64
	NodeUuidBySlot  []uuid.UUID
	ReverbPreset    int

	// NullOutputFactory, when set, builds the output for NullOut nodes
	// (e.g. performance-test "people"). It lets a backend supply a
	// binaural-decoding-then-discard output so test listeners exercise the
	// binaural render path. nil (default) → a no-op StereoNullOutput, so
	// behaviour is unchanged for any backend that does not opt in.
	NullOutputFactory func(channelCount int) inout.AmbisonicOutput

	opChanges   map[opKey]statecache.Op
	opChangesMu sync.Mutex

	// Space-wide role state. Mutated only on the audio thread from
	// processOpChanges (per Phase 2 design — no lock needed since reads
	// also happen on the audio thread inside the predicate).
	mutedRoles       mapset.Set[string]
	roleGains        map[string]float64
	roleAttenuations map[string]float64
}

type opKey struct {
	topic string
	key   string
}

func NewBaseSpace(name string,
	size float64,
	order int,
	MaxSources int,
	TimeoutTicks int64,
	reverbPreset int) BaseSpace {

	space := BaseSpace{Name: name,
		Size:           size,
		AmbisonicOrder: order,
		MaxSources:     MaxSources,
		OpenToChange:   false,
		ReverbPreset:   reverbPreset}

	channelCount := common.ChannelCountForOrder(order)

	space.Nodes = make(map[uuid.UUID]*Node)
	space.changesQueue = make(chan NodeChange, 1500)
	space.DoneCh = make(chan int, 1500)
	space.Wg = &sync.WaitGroup{}
	space.ChannelCount = channelCount
	space.GlobalCount = 0
	space.OutputCount = 0
	space.ticker = 0
	space.ForceGlobals = false
	space.UsingGroups = false
	space.TimeoutTicks = TimeoutTicks
	space.GlobalBuffer = make([]float32, common.FRAME_SIZE)
	space.opChanges = make(map[opKey]statecache.Op)
	space.mutedRoles = mapset.NewSet[string]()
	space.roleGains = make(map[string]float64)
	space.roleAttenuations = make(map[string]float64)
	return space
}

func (space *BaseSpace) Self() *BaseSpace {
	return space
}

func (space *BaseSpace) BeforeDestroy() {
	for _, node := range space.Nodes {
		node.BeforeDestroy()
	}
	space.GlobalBuffer = nil
}

func (space *BaseSpace) IsCurrentlyCustering() bool {
	return false
}

func (space *BaseSpace) GetTick() int64 {
	return space.ticker
}

func (space *BaseSpace) GetNodes() *(map[uuid.UUID]*Node) {
	return &space.Nodes
}

func (space *BaseSpace) AsJson() common.J {
	return common.J{"name": space.Name,
		"node_count": len(space.Nodes)}
}

func (mixer *BaseSpace) AddNodeToSlots(name uuid.UUID) int {

	//fmt.Printf("len slots: %d\n", len(mixer.NodeNamesBySlot))
	//first look fo an empty slot
	for i, n := range mixer.NodeUuidBySlot {
		if n == uuid.Nil {
			mixer.NodeUuidBySlot[i] = name
			//fmt.Printf("addNodeToSlots: %d\n", i)
			return i
		}
	}
	// otherwise add one to the end
	mixer.NodeUuidBySlot = append(mixer.NodeUuidBySlot, name)
	slot := len(mixer.NodeUuidBySlot) - 1
	//fmt.Printf("addNodeToSlots: %v %d\n", name, slot)
	return slot
}

func (mixer *BaseSpace) RemoveNodeFromSlots(slot int) {
	mixer.NodeUuidBySlot[slot] = uuid.Nil
}

func (space *BaseSpace) Process(doPartition bool) int {

	space.ticker += 1
	space.checkForStaleNodes()

	space.processOpChanges()

	if len(space.changesQueue) > 0 {
		space.StartChanges()
		space.processChanges()
		space.EndChanges()
	}
	return 1
}

func (space *BaseSpace) CollectGlobals() {
	space.doCollectGlobals(space.ForceGlobals)
}

func (space *BaseSpace) doCollectGlobals(force bool) {

	for i := 0; i < len(space.GlobalBuffer); i++ {
		space.GlobalBuffer[i] = 0
	}

	space.GlobalCount = 0
	for _, node := range space.Nodes {
		if node.global || force {

			nodeSource := node.Encoder.Input
			for i := 0; i < len(space.GlobalBuffer); i++ {
				space.GlobalBuffer[i] = space.GlobalBuffer[i] + nodeSource[i]
			}
			space.GlobalCount++
		}
	}

	scale := float32(space.GlobalCount)

	for i := 0; i < len(space.GlobalBuffer); i++ {
		space.GlobalBuffer[i] = space.GlobalBuffer[i] / scale
	}

}

/////////////////////////////////////////////////////////////

func (space *BaseSpace) GetSessionIdForUuid(Uuid uuid.UUID) (uint64, bool) {
	return space.SourceManager.GetSessionIdForUuid(Uuid)
}

func (space *BaseSpace) SourceMaxReached() bool {
	return len(space.Nodes) >= space.MaxSources
}

func (space *BaseSpace) OutputMaxReached() bool {
	return space.SourceMaxReached()
}

func (space *BaseSpace) AddNodeStyledWithId(Uuid uuid.UUID,
	name string,
	position Position,
	config common.SpaceNodeConfig) {

	//fmt.Printf("AddNodeStyledWithId: %v\n", config)

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_ADD,
		Position: position,
		Name:     name,
		Uuid:     Uuid,
		Config:   config}
}

func (space *BaseSpace) EnableOut(Uuid uuid.UUID) {

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_ENABLE_OUT, Uuid: Uuid}
}

func (space *BaseSpace) EnsureSpaceSource(Uuid uuid.UUID, isRaw bool, hasInput bool) uint64 {
	return space.SourceManager.EnsureSource(Uuid, space, space, isRaw, hasInput)
}

///////////////////////////////////////

func (space *BaseSpace) GetNode(Uuid uuid.UUID) (*Node, error) {

	node, exists := space.Nodes[Uuid]
	if !exists {
		err := NewSpaceError(ERROR_NODE_NAME_MISSING, map[string]string{"uuid": Uuid.String()})
		err.StatusCode = 404
		return nil, err
	} else {
		return node, nil
	}
}

///////////////////////////////////////////////////////////////////////

func (space *BaseSpace) UpdateNode(Uuid uuid.UUID, position Position, rotation Rotation) error {

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_MOVE,
		Position: position,
		Rotation: rotation,
		Uuid:     Uuid}

	return nil
}

func (space *BaseSpace) DeleteNode(Uuid uuid.UUID) {
	space.changesQueue <- NodeChange{Command: NODE_CHANGE_DELETE, Uuid: Uuid}
}

///////////// internal

// / BaseSpace Changes
func (space *BaseSpace) clearChanges() {

	//if !BaseSpace.Direct {
	//	for i := 0; i < BaseSpace.Depth; i++ {
	//		level := BaseSpace.levels[i]
	//		level.Added = make(map[Index]*Cell)
	//		level.Removed = make(map[Index]*Cell)
	//	}
	//}
}

func (space *BaseSpace) StartChanges() {
	space.clearChanges()
	space.OpenToChange = true
}

func (space *BaseSpace) EndChanges() {
	space.OpenToChange = false
}

func (space *BaseSpace) processChange(change NodeChange) {
	switch change.Command {
	case NODE_CHANGE_ADD:
		space.doAddNode(change.Name, change.Position, change.Uuid, change.Config)
	case NODE_CHANGE_MOVE:
		space.doMoveNode(change.Uuid, change.Position, change.Rotation)
	case NODE_CHANGE_DELETE:
		space.doRemoveNode(change.Uuid)
	case NODE_CHANGE_ENABLE_OUT:
		space.doEnableOutputNode(change.Uuid)
	case NODE_CHANGE_SOLO:
		space.doSoloNode(change.Uuid, change.OtherUuid)
	case NODE_CHANGE_UNSOLO:
		space.doUnsoloNode(change.Uuid, change.OtherUuid)
	case NODE_CHANGE_ADD_SUB:
		space.doAddSubspace(change.Uuid, change.OtherUuid)
	case NODE_CHANGE_REMOVE_SUB:
		space.doRemoveSubspace(change.Uuid, change.OtherUuid)
	}

}

func (space *BaseSpace) processChanges() {
	for len(space.changesQueue) > 0 {
		space.processChange(<-space.changesQueue)
	}
}

func (space *BaseSpace) AddNodeQ(name string, position Position) {

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_ADD,
		Position:    position,
		Name:        name,
		Gain:        1.0,
		Attenuation: 2.0}
}

func (space *BaseSpace) AddNodeQStyled(name string, position Position, style map[string]string, config common.SpaceNodeConfig) {

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_ADD,
		Uuid:        uuid.New(),
		Position:    position,
		Gain:        1.0,
		Attenuation: 2.0,
		Name:        name,
		Style:       style,
		Config:      config,
	}
}

func (space *BaseSpace) AddNode(name string, position Position) error {
	space.AddNodeQ(name, position)
	space.processChanges()
	return nil
}

func (space *BaseSpace) doAddNode(name string,
	position Position,
	Uuid uuid.UUID,
	config common.SpaceNodeConfig) error {

	if !space.OpenToChange {
		return NewSpaceError(ERROR_CUBE_CLOSED, nil)
	}

	_, exists := space.Nodes[Uuid]
	if exists {
		return NewSpaceError(ERROR_NODE_NAME_DUPLICATE, map[string]string{"name": name})
	} else {
		common.LogInfo("Adding node: %v %v %v", name, Uuid, config)
		node := NewNode(Uuid, name, space, position, space.ChannelCount, config)
		space.Nodes[Uuid] = node
		//BaseSpace.changedNodes[name] = node
		node.SetSlot(space.AddNodeToSlots(node.Uuid), space.ReverbPreset)
		//filters here?
		for _, subSpace := range config.SubSpaces {
			node.Encoder.AddSubSpace(subSpace)
		}

		return nil
	}
}

func (space *BaseSpace) SoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID) {
	space.changesQueue <- NodeChange{Command: NODE_CHANGE_SOLO,
		Uuid:      nodeId,
		OtherUuid: otherNodeId,
	}
}

func (space *BaseSpace) doSoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID) {
	node, exists := space.Nodes[nodeId]
	if exists {
		node.Encoder.AddSolo(otherNodeId)
	}
}

func (space *BaseSpace) UnsoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID) {
	space.changesQueue <- NodeChange{Command: NODE_CHANGE_UNSOLO,
		Uuid:      nodeId,
		OtherUuid: otherNodeId,
	}
}

func (space *BaseSpace) doUnsoloNode(nodeId uuid.UUID, otherNodeId uuid.UUID) {
	node, exists := space.Nodes[nodeId]
	if exists {
		node.Encoder.RemoveSolo(otherNodeId)
	}
}

func (space *BaseSpace) AddSubspace(nodeId uuid.UUID, subspaceId uuid.UUID) {
	space.changesQueue <- NodeChange{Command: NODE_CHANGE_ADD_SUB,
		Uuid:      nodeId,
		OtherUuid: subspaceId,
	}
}

func (space *BaseSpace) doAddSubspace(nodeId uuid.UUID, subspaceId uuid.UUID) {
	node, exists := space.Nodes[nodeId]
	if exists {
		node.Encoder.AddSubSpace(subspaceId)
	}
}

func (space *BaseSpace) RemoveSubspace(node uuid.UUID, subspaceId uuid.UUID) {
	space.changesQueue <- NodeChange{Command: NODE_CHANGE_REMOVE_SUB,
		Uuid:      node,
		OtherUuid: subspaceId,
	}
}

func (space *BaseSpace) doRemoveSubspace(nodeId uuid.UUID, subspaceId uuid.UUID) {
	node, exists := space.Nodes[nodeId]
	if exists {
		node.Encoder.RemoveSubSpace(subspaceId)
	}
}

func (space *BaseSpace) MoveNode(Uuid uuid.UUID, position Position) error {
	space.MoveNodeQ(Uuid, position)
	space.processChanges()
	return nil
}

func (space *BaseSpace) MoveNodeQ(Uuid uuid.UUID, position Position) {

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_MOVE,
		Position: position,
		Uuid:     Uuid}
}

func (space *BaseSpace) doMoveNode(Uuid uuid.UUID, position Position, rotation Rotation) error {

	if !space.OpenToChange {
		fmt.Printf("doMoveNode CLOSED WTF: %v\n", position)
		return NewSpaceError(ERROR_CUBE_CLOSED, nil)
	}

	//fmt.Printf("doMoveNode 1: %v\n", position)

	node, exists := space.Nodes[Uuid]
	if !exists {
		return NewSpaceError(ERROR_NODE_NAME_MISSING, map[string]string{"uuid": Uuid.String()})
	} else {
		if node.Rotation != rotation {
			node.SetRotation(rotation)
			space.SourceManager.SetRotationForUuid(rotation, Uuid)
		}
		if node.Position != position {
			node.SetPosition(position)
		}
	}
	return nil
}

// StaleNodeNotifier is an optional ISourceManager extension
// (plan/history/state-cleanup phase 4): when implemented, a stale node is
// surfaced to the backend — which responds with a transport Kill, and
// the departure funnel does the rest — instead of being silently
// removed here. Backends without it (cloud, until phase 6) keep the
// legacy direct removal.
type StaleNodeNotifier interface {
	NotifyStaleNode(Uuid uuid.UUID)
}

func (space *BaseSpace) checkForStaleNodes() {

	// looks for any Nodes that have not recieved new Data for more than 400 ticks ie 2 seconds
	notifier, hasNotifier := space.SourceManager.(StaleNodeNotifier)
	for _, node := range space.Nodes {
		if node.tick != 0 && node.tick < space.ticker-space.TimeoutTicks {
			if hasNotifier {
				// Activity timeout is a Kill cause, not a removal path:
				// the backend severs the transport and the funnel runs
				// the full announced departure (previously: silent
				// vanish with no entity tombstone or Gone).
				notifier.NotifyStaleNode(node.Uuid)
			} else {
				space.RemoveNodeQ(node.Uuid)
			}
		}
	}
}

func (space *BaseSpace) RemoveNodeQ(Uuid uuid.UUID) {

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_DELETE, Uuid: Uuid}
}

func (space *BaseSpace) RemoveNode(Uuid uuid.UUID) error {
	space.RemoveNodeQ(Uuid)
	space.processChanges()
	return nil
}

func (space *BaseSpace) doRemoveNode(Uuid uuid.UUID) error {

	if !space.OpenToChange {
		return NewSpaceError(ERROR_CUBE_CLOSED, nil)
	}

	node, exists := space.Nodes[Uuid]
	if !exists {
		return NewSpaceError(ERROR_NODE_NAME_MISSING, map[string]string{"uuid": Uuid.String()})
	} else {

		sessionId, exists := space.SourceManager.GetSessionIdForUuid(Uuid)

		if exists {
			space.NotifySessionGone(sessionId)
		}

		common.LogDebug("removing node: %v", Uuid)
		node.BeforeDestroy()
		delete(space.Nodes, node.Uuid)
		space.RemoveNodeFromSlots(node.SlotIndex)
		return nil
	}
}

func (space *BaseSpace) EnableOutputNodeQ(Uuid uuid.UUID) {

	space.changesQueue <- NodeChange{Command: NODE_CHANGE_ENABLE_OUT, Uuid: Uuid}
}

func (space *BaseSpace) EnableOutputNode(Uuid uuid.UUID) error {
	space.EnableOutputNodeQ(Uuid)
	space.processChanges()
	return nil
}

func (space *BaseSpace) doEnableOutputNode(Uuid uuid.UUID) error {

	if !space.OpenToChange {
		return NewSpaceError(ERROR_CUBE_CLOSED, nil)
	}

	//fmt.Printf("doEnableOutputNode: %v\n", Uuid)

	node, exists := space.Nodes[Uuid]
	if !exists {
		return NewSpaceError(ERROR_NODE_NAME_MISSING, map[string]string{"uuid": Uuid.String()})
	} else {
		common.LogDebug("Enabling output node: %v", Uuid)
		node.EnableUdpOutput()
		return nil
	}
}

////////////////////////////////////////////

func (space *BaseSpace) getAudioInput(Uuid uuid.UUID) inout.MonoInput {
	return space.SourceManager.GetInput(Uuid)
}

func (space *BaseSpace) getAudioOutput(Uuid uuid.UUID) inout.AmbisonicOutput {
	return space.SourceManager.GetOutput(Uuid)
}

func (space *BaseSpace) freeAudioInputOutput(Uuid uuid.UUID) {
	if space.SourceManager != nil {
		space.SourceManager.FreeSource(Uuid)
	}
}

func (space *BaseSpace) NotifyReadMiss(miss int) {
	if space.StatsDelegate != nil {
		space.StatsDelegate.NotifyReadMiss(miss)
	}
}

func (space *BaseSpace) NotifySessionGone(sessionId uint64) {
	if space.StatsDelegate != nil {
		//fmt.Printf("NotifySessionGone sessionId: %d", sessionId)
		space.StatsDelegate.NotifySessionGone(sessionId)
	}
}
