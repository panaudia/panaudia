package inout

import (
	"log"
	"unsafe"

	"github.com/panaudia/panaudia/core/binaural"
	"github.com/panaudia/panaudia/core/buffers"
	"github.com/panaudia/panaudia/core/common"
	"gopkg.in/hraban/opus.v2"
)

type OutputEncoder interface {
	Encode(ambisonicChannels []float32) []byte
	SetRotation(rotation common.Rotation)
}

type BytesOutputEncoder struct {
	channelCount int
}

func NewBytesOutputEncoder(channelCount int) *BytesOutputEncoder {
	encoder := BytesOutputEncoder{}
	encoder.channelCount = channelCount
	return &encoder
}

func (encoder *BytesOutputEncoder) Encode(ambisonicChannels []float32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&ambisonicChannels[0])), len(ambisonicChannels)*4)
}

func (encoder *BytesOutputEncoder) SetRotation(rotation common.Rotation) {

}

type OpusOutputEncoder struct {
	BinauralDecoder         *binaural.BinauralDecoder
	opusEncoder             *opus.Encoder
	opusOutputBuffer        []byte
	singleSinkBuffer        *buffers.CBuffer
	AmbisonicBufferPointers []uintptr
	dynamics                *StereoCompressorLimiter
	testTone                *stereoTestTone
}

func NewOpusOutputEncoder(binauralDecoder *binaural.BinauralDecoder, channelCount int) *OpusOutputEncoder {
	outputEncoder := OpusOutputEncoder{}
	outputEncoder.BinauralDecoder = binauralDecoder
	outputEncoder.opusOutputBuffer = make([]byte, 10000)

	outputEncoder.AmbisonicBufferPointers = make([]uintptr, channelCount)
	outputEncoder.singleSinkBuffer = buffers.NewCBuffer(common.FRAME_SIZE * channelCount)

	firstPointer := outputEncoder.singleSinkBuffer.GetDataPointer()

	for i := 0; i < channelCount; i++ {
		outputEncoder.AmbisonicBufferPointers[i] = firstPointer + (uintptr(i) * common.FRAME_SIZE * 4)
	}

	opusEncoder, err := opus.NewEncoder(common.SAMPLE_RATE, 2, opus.AppAudio)
	if err != nil {
		panic(err)
	}
	err2 := opusEncoder.SetBitrate(96000)
	if err2 != nil {
		common.LogError("Error setting opus output: %v", err2)
	}
	err3 := opusEncoder.SetInBandFEC(true)
	if err3 != nil {
		common.LogError("Error SetInBandFEC: %v", err3)
	}
	outputEncoder.opusEncoder = opusEncoder
	outputEncoder.dynamics = NewStereoCompressorLimiter(float32(common.SAMPLE_RATE), common.FRAME_SIZE)

	if stereoTestToneEnabled {
		outputEncoder.testTone = newStereoTestTone()
	}

	return &outputEncoder
}

func (outputEncoder *OpusOutputEncoder) BeforeDestroy() {
	outputEncoder.BinauralDecoder.Release()
	outputEncoder.singleSinkBuffer.BeforeDestroy()
}

func (outputEncoder *OpusOutputEncoder) Encode(ambisonicChannels []float32) []byte {

	outputEncoder.singleSinkBuffer.CopyFromSlice(ambisonicChannels)
	outputEncoder.BinauralDecoder.AmbisonicToStereo(outputEncoder.AmbisonicBufferPointers)

	stereoData := outputEncoder.BinauralDecoder.StereoBuffer.AsUnsafeFloatSlice()

	if outputEncoder.testTone != nil {
		outputEncoder.testTone.Fill(stereoData)
	}

	outputEncoder.dynamics.Process(stereoData)

	nOut, err := outputEncoder.opusEncoder.EncodeFloat32(stereoData,
		outputEncoder.opusOutputBuffer)
	if err != nil {
		log.Print("encode failed")
		panic(err)
	}

	return outputEncoder.opusOutputBuffer[:nOut]
}

func (encoder *OpusOutputEncoder) SetRotation(rotation common.Rotation) {
	encoder.BinauralDecoder.UpdateRotation(rotation)
}
