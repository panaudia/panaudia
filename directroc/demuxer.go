package directroc

import (
	"sync/atomic"
	"time"

	// 	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/space"
)

type Demuxer struct {
	buffers        [][]float32
	TrackInputs    []*RocTrackInput
	BouncerClients []*space.BouncerClient
	trackCount     int
	size           int

	// lastPacket is the unix-nano time of the most recent inbound
	// frame — ROC is connectionless, so packet activity is the only
	// liveness signal its sessions have (plan/history/state-cleanup phase 2).
	lastPacket atomic.Int64
}

// LastPacketAge returns the time since the last demuxed inbound frame;
// a very large duration if none has arrived yet.
func (demuxer *Demuxer) LastPacketAge() time.Duration {
	ts := demuxer.lastPacket.Load()
	if ts == 0 {
		return time.Duration(1<<63 - 1)
	}
	return time.Since(time.Unix(0, ts))
}

func NewDemuxer(trackCount int, size int) *Demuxer {

	buffers := make([][]float32, trackCount)
	trackInputs := make([]*RocTrackInput, trackCount)

	for i := 0; i < trackCount; i++ {
		buffers[i] = make([]float32, size)
		trackInputs[i] = NewRocTrackInput()
	}

	return &Demuxer{trackCount: trackCount, TrackInputs: trackInputs, buffers: buffers, size: size}
}

func (demuxer *Demuxer) WriteDemuxf32(src []float32) error {

	demuxer.lastPacket.Store(time.Now().UnixNano())

	size := len(src) / demuxer.trackCount

	//common.LogVerbose("demuxer size: %d", size)
	//common.LogVerbose("demuxer src: %v", src)

	if size > demuxer.size {
		panic("Buffer size is too small")
	}

	for i := 0; i < size; i++ {
		for j := 0; j < demuxer.trackCount; j++ {
			demuxer.buffers[j][i] = src[i*demuxer.trackCount+j]
		}
	}

	for k := 0; k < demuxer.trackCount; k++ {
		demuxer.TrackInputs[k].Writef32(demuxer.buffers[k][:size])
		if len(demuxer.BouncerClients) > k {
			demuxer.BouncerClients[k].SetVolume(demuxer.buffers[k][:size])
		}
	}

	return nil
}
