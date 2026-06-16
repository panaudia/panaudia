package direct

import (
	"github.com/panaudia/panaudia/core/ambisonic"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/space"
)

type DirectSpace struct {
	space.BaseSpace
}

func NewDefaultDirectSpace(size float64, order int, reverbPreset int, maxSources int) *DirectSpace {
	return NewDirectSpace("default",
		size,
		order,
		8,
		maxSources,
		100,
		reverbPreset,
	)
}

func NewDirectSpace(name string,
	size float64,
	order int,
	workerCount int,
	MaxSources int,
	TimeoutTicks int64,
	reverbPreset int) *DirectSpace {

	directSpace := DirectSpace{}

	directSpace.BaseSpace = space.NewBaseSpace(name,
		size,
		order,
		MaxSources,
		TimeoutTicks,
		reverbPreset)

	mixerConfig := common.MixerConfig{
		MaxNodes:     MaxSources,
		FrameSize:    common.FRAME_SIZE,
		ChannelCount: common.ChannelCountForOrder(order),
		Order:        order,
		Size:         size,
	}

	directSpace.Renderer = space.NewRenderer(workerCount, directSpace.Wg, mixerConfig)

	common.LogInfo("Ready to receive connections")

	return &directSpace
}

func (directSpace *DirectSpace) render() {

	directSpace.NodeMixCount = 0
	directSpace.GlobalCount = 0

	var encoders []*ambisonic.Encoder
	for _, node := range directSpace.Nodes {
		if node.Input != nil {
			encoders = append(encoders, node.Encoder)
		}
	}
	directSpace.AllNodeEncoders = encoders
	directSpace.Renderer.Render(&directSpace.BaseSpace)
}

func (directSpace *DirectSpace) Process(doPartition bool) int {
	directSpace.BaseSpace.Process(doPartition)
	directSpace.render()
	return 1
}
