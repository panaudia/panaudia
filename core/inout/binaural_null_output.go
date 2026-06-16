package inout

import (
	"github.com/panaudia/panaudia/core/binaural"
	"github.com/panaudia/panaudia/core/buffers"
	"github.com/panaudia/panaudia/core/common"
)

// BinauralNullOutput runs the full binaural decode (SAF ambi_bin_process via
// the BinauralDecoder) on each frame and then discards the stereo result.
// Unlike StereoNullOutput — which drops the ambisonic field without decoding —
// it exercises the binaural render path, so it is used as the output for
// performance-test "people" whose audio is never actually sent anywhere.
type BinauralNullOutput struct {
	binauralDecoder         *binaural.BinauralDecoder
	singleSinkBuffer        *buffers.CBuffer
	ambisonicBufferPointers []uintptr
}

func NewBinauralNullOutput(binauralDecoder *binaural.BinauralDecoder, channelCount int) *BinauralNullOutput {
	output := BinauralNullOutput{}
	output.binauralDecoder = binauralDecoder
	output.singleSinkBuffer = buffers.NewCBuffer(common.FRAME_SIZE * channelCount)

	output.ambisonicBufferPointers = make([]uintptr, channelCount)
	firstPointer := output.singleSinkBuffer.GetDataPointer()
	for i := 0; i < channelCount; i++ {
		output.ambisonicBufferPointers[i] = firstPointer + (uintptr(i) * common.FRAME_SIZE * 4)
	}

	return &output
}

func (output *BinauralNullOutput) WriteAmbisonic(ambisonicChannels []float32) {
	if output.binauralDecoder == nil {
		return
	}
	output.singleSinkBuffer.CopyFromSlice(ambisonicChannels)
	// Decode to binaural stereo; the result (decoder.StereoBuffer) is left
	// untouched — this is a sink that exists only to spend the CPU.
	output.binauralDecoder.AmbisonicToStereo(output.ambisonicBufferPointers)
}

func (output *BinauralNullOutput) BeforeDestroy() {
	if output.binauralDecoder != nil {
		output.binauralDecoder.Release()
	}
	output.singleSinkBuffer.BeforeDestroy()
}
