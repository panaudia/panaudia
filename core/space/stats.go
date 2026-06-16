package space

import (
	"encoding/json"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/inout"
)

type MixerStats struct {
	SpaceId      string      `json:"space_id"`
	SpaceName    string      `json:"space_name"`
	HostIp       string      `json:"host_ip"`
	CubeSize     int         `json:"cube_size"`
	CubeOrder    int         `json:"cube_order"`
	Direct       bool        `json:"direct"`
	GoMaxProcs   int         `json:"go_max_procs"`
	InputCount   int         `json:"input_count"`
	OutputCount  int         `json:"output_count"`
	MsMax        int         `json:"ms_max"`
	MsAverage    int         `json:"ms_average"`
	IsClustering bool        `json:"is_clustering"`
	Nodes        []NodeStats `json:"Nodes"`
	ReadMisses   int         `json:"read_misses"`
	LateMisses   int         `json:"late_misses"`
	FrameMisses  int         `json:"frame_misses"`
}

type NodeStats struct {
	Uuid     string          `json:"uuid"`
	Name     string          `json:"name"`
	Position common.Position `json:"position"`
	Input    bool            `json:"Input"`
	Output   bool            `json:"ReverbOutput"`
	Cluster  int             `json:"cluster"`
}

type MixerStatsSender struct {
	messageSender   IMessageSender
	SpaceId         string
	SpaceName       string
	HostIp          string
	CubeSize        int
	CubeOrder       int
	Direct          bool
	GoMaxProcs      int
	Nodes           []NodeStats
	readMissesQueue chan int
	ReadMisses      int
	LateMisses      int
	Done            bool
}

func NewMixerStatsSender(messageSender IMessageSender,
	SpaceId string,
	SpaceName string,
	HostIp string,
	CubeSize int,
	CubeOrder int,
	MaxSources int,
	Direct bool,
	GoMaxProcs int) *MixerStatsSender {
	sender := MixerStatsSender{
		messageSender: messageSender,
		SpaceId:       SpaceId,
		SpaceName:     SpaceName,
		HostIp:        HostIp,
		CubeSize:      CubeSize,
		CubeOrder:     CubeOrder,
		Direct:        Direct,
		GoMaxProcs:    GoMaxProcs}

	sender.Nodes = make([]NodeStats, MaxSources)
	sender.readMissesQueue = make(chan int, 200)
	pSender := &sender

	go func() {
		for !pSender.Done {
			miss := <-pSender.readMissesQueue
			if miss == 1 {
				pSender.ReadMisses += 1
			}
			if miss == 2 {
				pSender.LateMisses += 1
			}
		}
	}()

	return pSender
}

func (sender *MixerStatsSender) SendStatsForCube(iSpace ISpace, msMax int, msAverage int, frameMisses int) {

	inputCount := 0
	outputCount := 0
	counter := 0
	ReadMisses := sender.ReadMisses
	LateMisses := sender.LateMisses
	sender.ReadMisses = 0
	sender.LateMisses = 0

	for Uuid, node := range iSpace.Self().Nodes {
		sender.Nodes[counter].Uuid = Uuid.String()
		sender.Nodes[counter].Name = node.Name
		sender.Nodes[counter].Position = node.Position
		sender.Nodes[counter].Input = node.Input != nil
		sender.Nodes[counter].Output = node.Output != nil
		sender.Nodes[counter].Cluster = node.ClusterIndex
		if sender.Nodes[counter].Input {
			inputCount++
		}
		if sender.Nodes[counter].Output {
			outputCount++
		}
		counter++
	}

	stats := MixerStats{SpaceId: sender.SpaceId,
		SpaceName:    sender.SpaceName,
		HostIp:       sender.HostIp,
		CubeSize:     sender.CubeSize,
		CubeOrder:    sender.CubeOrder,
		Direct:       sender.Direct,
		GoMaxProcs:   sender.GoMaxProcs,
		InputCount:   inputCount,
		OutputCount:  outputCount,
		IsClustering: iSpace.IsCurrentlyCustering(),
		Nodes:        sender.Nodes[:0],
		MsMax:        msMax,
		MsAverage:    msAverage,
		ReadMisses:   ReadMisses,
		LateMisses:   LateMisses,
		FrameMisses:  frameMisses}

	go func() {
		common.LogDebug("%v - avg ms: %d", sender.HostIp, msAverage)
		if buffer, err := json.Marshal(stats); err != nil {
			common.LogCritical("panic sending stats: %v", err)
		} else {
			sender.messageSender.SendString("stats", string(buffer))
		}
	}()
}

func (sender *MixerStatsSender) NotifyReadMiss(miss int) {
	sender.readMissesQueue <- miss
}

func (sender *MixerStatsSender) NotifySessionGone(sessionId uint64) {
	sender.messageSender.SendData("session", inout.EncodeUint64(sessionId))
}
