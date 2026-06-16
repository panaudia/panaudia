package directroc

import (
	"time"

	"github.com/panaudia/panaudia/core/buffers"
)

type RocTrackInput struct {
	buffer buffers.ICircularBuffer
}

func NewRocTrackInput() *RocTrackInput {
	input := RocTrackInput{}
	// ROC writes 10 ms blocks; the reader pulls 5 ms callbacks. Network
	// jitter on ROC is typically low (network audio over UDP with FEC),
	// so a moderate LowInit warm-start is sufficient (L adapts from there).
	input.buffer = buffers.NewJitterBuffer(buffers.JitterBufferConfig{
		SampleRate:      48000,
		NumChannels:     1,
		WriterFrameSize: 10 * time.Millisecond,
		ReaderFrameSize: 5 * time.Millisecond,
		LowInit:         10 * time.Millisecond,
	})
	return &input
}

func (input *RocTrackInput) ReadMono(dst []float32) {
	input.buffer.Read(dst)
}

func (input *RocTrackInput) Writef32(src []float32) error {
	input.buffer.Write(src)
	return nil
}

func (input *RocTrackInput) GetTick() int64 {
	return 0
}

func (input *RocTrackInput) BeforeDestroy() {

}
