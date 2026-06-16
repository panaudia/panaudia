package roc

import (
	"fmt"
	"time"

	"github.com/panaudia/panaudia/core/common"
	"github.com/roc-streaming/roc-go/roc"
)

type RocOutput struct {
	trackCount   uint32
	chunkSize    int
	host         string
	sourcePort   int
	repairPort   int
	controlPort  int
	stop         bool
	senderConfig roc.SenderConfig
	sender       *roc.Sender
	ctx          *roc.Context
}

func NewRocOutput(ports common.RocPorts, trackCount uint32) *RocOutput {
	senderConfig := GetConfig(trackCount)
	return &RocOutput{trackCount: trackCount,
		chunkSize:    int(trackCount * 60),
		host:         ports.Host,
		sourcePort:   int(ports.Source),
		repairPort:   int(ports.Repair),
		controlPort:  int(ports.Control),
		senderConfig: senderConfig,
		stop:         false,
	}
}

func GetConfig(trackCount uint32) roc.SenderConfig {

	return roc.SenderConfig{
		FrameEncoding: roc.MediaEncoding{
			Rate:     48000,
			Format:   roc.FormatPcmFloat32,
			Channels: roc.ChannelLayoutMultitrack,
			Tracks:   9,
		},
		PacketEncoding: roc.PacketEncoding(30),
		FecEncoding:    roc.FecEncodingRs8m,
		ClockSource:    roc.ClockSourceExternal,
		PacketLength:   time.Duration(2000) * time.Microsecond,
	}
}

func (output *RocOutput) Start() {
	output.Connect()
}

func (output *RocOutput) Stop() {
	output.stop = true
	if output.sender != nil {
		output.sender.Close()
	}

	if output.ctx != nil {
		output.ctx.Close()
	}
}

func (output *RocOutput) Connect() {

	//roc.SetLogLevel(roc.LogTrace)

	//common.LogDebug("Connecting to roc track count: %d", output.trackCount)
	//common.LogDebug("host: %v", output.host)
	//common.LogDebug("sourcePort: %v", output.sourcePort)
	//common.LogDebug("repairPort: %v", output.repairPort)
	//common.LogDebug("controlPort: %v", output.controlPort)

	context, err := roc.OpenContext(roc.ContextConfig{MaxPacketSize: output.trackCount * 512, MaxFrameSize: output.trackCount * 1024})
	if err != nil {
		panic(err)
	}

	output.ctx = context

	encoding := roc.MediaEncoding{Rate: 48000,
		Format:   roc.FormatPcmFloat32,
		Channels: roc.ChannelLayoutMultitrack,
		Tracks:   output.trackCount}

	encodingId := 30

	err2 := context.RegisterEncoding(int(encodingId), encoding)
	if err2 != nil {
		panic(err2)
	}

	sender, err := roc.OpenSender(context, output.senderConfig)
	if err != nil {
		panic(err)
	}

	sourceEndpoint, err := roc.ParseEndpoint(fmt.Sprintf("rtp+rs8m://%s:%d", output.host, output.sourcePort))
	if err != nil {
		panic(err)
	}

	repairEndpoint, err := roc.ParseEndpoint(fmt.Sprintf("rs8m://%s:%d", output.host, output.repairPort))
	if err != nil {
		panic(err)
	}

	controlEndpoint, err := roc.ParseEndpoint(fmt.Sprintf("rtcp://%s:%d", output.host, output.controlPort))
	if err != nil {
		panic(err)
	}

	err = sender.Connect(roc.SlotDefault, roc.InterfaceAudioSource, sourceEndpoint)
	if err != nil {
		panic(err)
	}

	err = sender.Connect(roc.SlotDefault, roc.InterfaceAudioRepair, repairEndpoint)
	if err != nil {
		panic(err)
	}

	err = sender.Connect(roc.SlotDefault, roc.InterfaceAudioControl, controlEndpoint)
	if err != nil {
		panic(err)
	}

	output.sender = sender
}

func (output *RocOutput) Writef32(samples []float32) {

	//defer roc.SetLogLevel(roc.LogInfo)

	//common.LogDebug("writeLength: %v", len(samples))

	if !output.stop {
		if output.sender != nil {

			chunk := 0
			size := output.chunkSize

			for {

				start := chunk * size
				if start >= len(samples) {
					break
				}

				end := (chunk + 1) * size
				if end > len(samples) {
					output.Writef32Chunk(samples[start:])
				} else {
					output.Writef32Chunk(samples[start:end])
				}
				chunk++
			}
		}
	}
}

func (output *RocOutput) Writef32Chunk(samples []float32) {

	//common.LogDebug("samples: %v", samples)
	//roc.SetLogLevel(roc.LogTrace)
	if !output.stop {
		err := output.sender.WriteFloats(samples)
		if err != nil {
			panic(err)
		}
	}

	//common.LogDebug("Writef32 ambisonic")

}
