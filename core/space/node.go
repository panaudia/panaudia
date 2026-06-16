package space

import (
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/ambisonic"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
)

type Rotation = common.Rotation

type Node struct {
	Name string
	Uuid uuid.UUID
	Cube *BaseSpace
	//x, y, z are always In the range 0..1 where 1 = the side of whole Cube
	Position       Position
	SlotIndex      int
	targetPosition Position
	step           Position
	stepCounter    int
	Rotation       Rotation
	//gain           float64
	//attenuation    float64
	config common.SpaceNodeConfig
	//CellHandle     CellHandle
	channelCount int
	commandCh    chan int
	run          bool
	global       bool
	tick         int64
	MaxNodes     int
	SpaceSize    float64

	Input      inout.MonoInput
	Ticksource inout.TickSource

	Encoder *ambisonic.Encoder
	Output  inout.AmbisonicOutput
	Player  inout.AmbisonicOutput

	PeerNodeUids []uuid.UUID
	PeerEncoders []*ambisonic.Encoder
	ClusterIndex int
}

func NewNode(Uuid uuid.UUID,
	name string,
	space *BaseSpace,
	position Position,
	channelCount int,
	config common.SpaceNodeConfig) *Node {

	node := NewNodeBase(Uuid,
		name,
		space,
		position,
		channelCount,
		config)

	return &node
}

func NewNodeBase(Uuid uuid.UUID,
	name string,
	space *BaseSpace,
	position Position,
	channelCount int,
	config common.SpaceNodeConfig) Node {

	//fmt.Printf("config: %v", config)

	node := Node{Name: name,
		Uuid:         Uuid,
		Cube:         space,
		Position:     position,
		config:       config,
		channelCount: channelCount}

	node.commandCh = make(chan int, 1)
	node.PeerEncoders = make([]*ambisonic.Encoder, space.MaxSources)
	node.MaxNodes = space.MaxSources
	node.SpaceSize = space.Size

	node.Ticksource = space.getAudioInput(Uuid)

	if config.Input {
		node.Input = space.getAudioInput(Uuid)
	} else {
		if config.Tone > 0.0 {
			node.Input = inout.NewSineMonoInput(config.Tone, common.SAMPLE_RATE, common.FRAME_SIZE)
		}
	}

	if config.NullOut {
		if space.NullOutputFactory != nil {
			node.Output = space.NullOutputFactory(channelCount)
		} else {
			node.Output = inout.NewStereoNullOutput(channelCount)
		}
		space.OutputCount++
	}

	return node
}

func (node *Node) SetSlot(slot int, reverbPreset int) {
	node.SlotIndex = slot

	hasInput := !node.global && node.Input != nil

	mixerConfig := common.MixerConfig{
		MaxNodes:     node.MaxNodes,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: node.channelCount,
		Order:        common.OrderForChannelCount(node.channelCount),
		Size:         node.SpaceSize,
		ReverbPreset: reverbPreset,
	}

	node.Encoder = ambisonic.NewEncoder(node.Uuid,
		hasInput,
		node.config.Gain,
		node.config.Attenuation,
		mixerConfig,
		slot)

	//if node.Cube.NodeMixer.GetMaxSlots() < 1000 {
	//	fmt.Printf("GetMaxSlots: %s", node.Cube.NodeMixer.GetMaxSlots())
	//	panic("GetMaxSlots")
	//}

	node.Encoder.SetPosition(node.Position)
}

func (node *Node) SetPosition(position common.Position) {
	//fmt.Printf("SetPosition; %v\n", position)
	node.Position = position
	node.Encoder.SetPosition(position)
}

func (node *Node) SetRotation(rotation common.Rotation) {
	node.Rotation = rotation
}

func (node *Node) EnableUdpOutput() {
	//node.EnablePlayerOutput()

	node.Output = node.Cube.getAudioOutput(node.Uuid)
	node.Cube.OutputCount++
}

func (node *Node) DisableUdpOutput() {
	node.Output = nil
	node.Cube.OutputCount--
}

func (node *Node) BeforeDestroy() {

	if node.Output != nil {
		node.Cube.OutputCount--
	}
	//node.Encoder.BeforeDestroy()
	node.Cube.freeAudioInputOutput(node.Uuid)
}

func (node *Node) asJson() common.J {
	return common.J{"name": node.Name,
		"position": node.Position,
	}
}

func (node *Node) DoCommand(command int, mixer *ambisonic.Mixer, reverbMixer *ambisonic.Mixer) {

	switch command {
	case COMMAND_NODE_IN:
		node.In()
	case COMMAND_NODE_ACROSS:
		node.across(mixer, reverbMixer)
	case COMMAND_NODE_OUT:
		node.Out()
	}
}

func (node *Node) In() {
	//common.LogDebug("In")
	if node.Input != nil {
		node.Input.ReadMono(node.Encoder.Input)
	}
	if node.Ticksource != nil {
		//common.LogDebug("tick")
		node.tick = node.Ticksource.GetTick()
	} else {
		//common.LogError("no tick source")
	}
}

func (node *Node) across(mixer *ambisonic.Mixer, reverbMixer *ambisonic.Mixer) {

	if node.Output != nil {
		//common.LogDebug("across")
		node.Encoder.EncodePeers(node.Cube.AllNodeEncoders, mixer, reverbMixer)
	}
}

func (node *Node) Out() {
	if node.Cube.GlobalCount > 0 {
		node.Encoder.AddGlobalBuffer(node.Cube.GlobalBuffer)
	}

	//fmt.Printf("node.Cube.GlobalCount: %d", node.Cube.GlobalCount)

	if node.Output != nil {
		node.Encoder.PostMix()
		node.Output.WriteAmbisonic(node.Encoder.Output)
	}

}

func (node *Node) GetX() float64 {
	return node.Position.X
}
func (node *Node) GetY() float64 {
	return node.Position.Y
}
func (node *Node) GetUid() uuid.UUID {
	return node.Uuid
}
func (node *Node) GetPriority() bool {
	return node.config.Priority
}
