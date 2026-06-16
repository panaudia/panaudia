package roc

import (
	"fmt"
	"github.com/panaudia/panaudia/core/common"
	"github.com/roc-streaming/roc-go/roc"
)

type RocInput struct {
	slotRegistry   *SlotRegistry
	trackCount     uint32
	host           string
	Slot           uint32
	SourcePort     uint32
	RepairPort     uint32
	ControlPort    uint32
	receiverConfig roc.ReceiverConfig
	shouldStop     bool
	samples        []float32
	demuxer        Demuxerf32
}

func NewRocInput(host string,
	slotRegistry *SlotRegistry,
	trackCount uint32,
	demuxer Demuxerf32,
	frameSize uint32) *RocInput {
	receiverConfig := GetReceiverConfig(trackCount)
	slot := slotRegistry.NextSlot()

	srcPort := 20000 + (slot * 3)

	samples := make([]float32, frameSize*trackCount)
	return &RocInput{slotRegistry,
		trackCount,
		host,
		slot,
		srcPort,
		srcPort + 1,
		srcPort + 2,
		receiverConfig,
		false,
		samples,
		demuxer,
	}
}

func GetReceiverConfig(trackCount uint32) roc.ReceiverConfig {
	return roc.ReceiverConfig{
		FrameEncoding: roc.MediaEncoding{
			Rate:     48000,
			Format:   roc.FormatPcmFloat32,
			Channels: roc.ChannelLayoutMultitrack,
			Tracks:   trackCount,
		},
		ClockSource: roc.ClockSourceInternal,
	}
}

func (input *RocInput) Start() {
	go func() {
		input.Connect()
	}()
}

func (input *RocInput) Stop() {
	input.shouldStop = true
}

func (input *RocInput) Connect() {

	context, err := roc.OpenContext(roc.ContextConfig{MaxPacketSize: input.trackCount * 512, MaxFrameSize: input.trackCount * 1024})
	if err != nil {
		panic(err)
	}
	defer context.Close()

	encoding := roc.MediaEncoding{Rate: 48000,
		Format:   roc.FormatPcmFloat32,
		Channels: roc.ChannelLayoutMultitrack,
		Tracks:   input.trackCount}

	encodingId := input.trackCount + 40

	err2 := context.RegisterEncoding(int(encodingId), encoding)
	if err2 != nil {
		panic(err2)
	}

	receiver, err := roc.OpenReceiver(context, input.receiverConfig)
	if err != nil {
		panic(err)
	}
	defer receiver.Close()

	sourceEndpoint, err := roc.ParseEndpoint(fmt.Sprintf("rtp+rs8m://%s:%d", input.host, input.SourcePort))
	if err != nil {
		panic(err)
	}

	repairEndpoint, err := roc.ParseEndpoint(fmt.Sprintf("rs8m://%s:%d", input.host, input.RepairPort))
	if err != nil {
		panic(err)
	}

	controlEndpoint, err := roc.ParseEndpoint(fmt.Sprintf("rtcp://%s:%d", input.host, input.ControlPort))
	if err != nil {
		panic(err)
	}

	err = receiver.Bind(roc.Slot(input.Slot), roc.InterfaceAudioSource, sourceEndpoint)
	if err != nil {
		panic(err)
	}

	err = receiver.Bind(roc.Slot(input.Slot), roc.InterfaceAudioRepair, repairEndpoint)
	if err != nil {
		panic(err)
	}

	err = receiver.Bind(roc.Slot(input.Slot), roc.InterfaceAudioControl, controlEndpoint)
	if err != nil {
		panic(err)
	}

	common.LogDebug("ROC receiver listening")

	for !input.shouldStop {
		err = receiver.ReadFloats(input.samples)
		if err != nil {
			panic(err)
		}
		err2 := input.demuxer.WriteDemuxf32(input.samples)
		if err2 != nil {
			panic(err2)
		}
	}

	input.slotRegistry.FreeSlot(input.Slot)
}
