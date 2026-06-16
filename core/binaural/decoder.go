package binaural

import (
	"github.com/panaudia/panaudia/core/buffers"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/spacer"
	"math"
	"unsafe"
)

type BinauralDecoder struct {
	decoder_handle    uintptr
	order             int
	channelCount      int
	leftBuffer        *buffers.CBuffer
	rightBuffer       *buffers.CBuffer
	leftRightPointers []uintptr
	StereoBuffer      *buffers.CBuffer
	active            bool
}

func NewBinauralDecoder(channelCount int, rotation common.Rotation) *BinauralDecoder {

	bin := BinauralDecoder{}
	bin.channelCount = channelCount
	bin.order = int(math.Sqrt(float64(channelCount))) - 1
	bin.leftBuffer = buffers.NewCBuffer(common.FRAME_SIZE)
	bin.rightBuffer = buffers.NewCBuffer(common.FRAME_SIZE)
	bin.StereoBuffer = buffers.NewCBuffer(common.FRAME_SIZE * 2)
	bin.leftRightPointers = make([]uintptr, 2)
	bin.leftRightPointers[0] = bin.leftBuffer.GetDataPointer()
	bin.leftRightPointers[1] = bin.rightBuffer.GetDataPointer()
	bin.decoder_handle = makeDecoder(bin.order, rotation)
	bin.active = false
	return &bin
}

func (bin *BinauralDecoder) reset() {
	bin.leftBuffer.Clear()
	bin.rightBuffer.Clear()
	bin.StereoBuffer.Clear()
	bin.UpdateRotation(common.Rotation{})
	bin.active = false
}

func (bin *BinauralDecoder) claim() {
	bin.active = true
}

func (bin *BinauralDecoder) Release() {
	bin.active = false
	common.LogDebug("Releasing binaural decoder")
}

func (bin *BinauralDecoder) BeforeDestroy() {
	p := bin.decoder_handle
	spacer.Ambi_bin_destroy(&p)
	bin.leftBuffer.BeforeDestroy()
	bin.rightBuffer.BeforeDestroy()
	bin.StereoBuffer.BeforeDestroy()

}

func makeDecoder(order int, rotation common.Rotation) uintptr {
	p := uintptr(0)
	spacer.Ambi_bin_create(&p)
	spacer.Ambi_bin_init(p, common.SAMPLE_RATE)
	// the current ambisonic order
	spacer.Panaudia_utils_ambi_bin_setInputOrderPreset(p, order)
	//Sets the flag to enable/disable (1 or 0) sound-field rotation
	spacer.Ambi_bin_setEnableRotation(p, 1)
	spacer.Ambi_bin_setYaw(p, float32(rotation.Yaw))
	spacer.Ambi_bin_setPitch(p, float32(rotation.Pitch))
	spacer.Ambi_bin_setRoll(p, float32(rotation.Roll))

	// user the default HRIR
	spacer.Ambi_bin_setUseDefaultHRIRsflag(p, 1)

	// a bunch of things configured in the constants
	//decoding method
	spacer.Ambi_bin_setDecodingMethod(p, AMBI_BIN_DECODING_METHOD)
	// channel ordering in xyzw
	spacer.Ambi_bin_setChOrder(p, AMBI_CH_ORDER)
	// normalisation
	spacer.Ambi_bin_setNormType(p, AMBI_NORMALISATION_TYPE)
	// Sets a flag to enable/disable the max_rE weighting
	spacer.Ambi_bin_setEnableMaxRE(p, AMBI_BIN_ENABLE_MAX_RE)
	// Sets a flag to enable/disable (1 or 0) the diffuse-covariance constraint
	spacer.Ambi_bin_setEnableDiffuseMatching(p, AMBI_BIN_ENABLE_DIFFUSE_MATCHING)
	// Sets a flag to enable/disable (1 or 0) truncation EQ
	spacer.Ambi_bin_setEnableTruncationEQ(p, AMBI_BIN_ENABLE_TRUNCATION_EQ)
	// Sets HRIR pre-processing strategy (see #AMBI_BIN_PREPROC enum)
	spacer.Panaudia_utils_ambi_bin_setHRIRsPreProc(p, AMBI_BIN_HRIR_PREPROC)

	// these are here just in case we want them
	// Sets a flag as to whether to "flip" the sign of the current 'yaw' angle
	spacer.Ambi_bin_setFlipYaw(p, 0)
	// Sets a flag as to whether to "flip" the sign of the current 'pitch' angle
	spacer.Ambi_bin_setFlipPitch(p, 0)
	// Sets a flag as to whether to "flip" the sign of the current 'roll' angle
	spacer.Ambi_bin_setFlipRoll(p, 0)
	// Sets a flag as to whether to use "yaw-pitch-roll" (0) or "roll-pitch-yaw" (1)
	spacer.Ambi_bin_setRPYflag(p, 0)

	spacer.Ambi_bin_initCodec(p)

	return p
}

func (bin *BinauralDecoder) UpdateRotation(rotation common.Rotation) {
	spacer.Ambi_bin_setYaw(bin.decoder_handle, float32(rotation.Yaw))
	spacer.Ambi_bin_setPitch(bin.decoder_handle, float32(rotation.Pitch))
	spacer.Ambi_bin_setRoll(bin.decoder_handle, float32(rotation.Roll))
	spacer.Ambi_bin_initCodec(bin.decoder_handle)
}

// GetLeftBuffer returns the left channel buffer contents for debugging
func (bin *BinauralDecoder) GetLeftBuffer() []float32 {
	return bin.leftBuffer.AsUnsafeFloatSlice()
}

// GetRightBuffer returns the right channel buffer contents for debugging
func (bin *BinauralDecoder) GetRightBuffer() []float32 {
	return bin.rightBuffer.AsUnsafeFloatSlice()
}

func (bin *BinauralDecoder) AmbisonicToStereo(src []uintptr) {
	p_src := uintptr(unsafe.Pointer(&src[0]))
	p_dst := uintptr(unsafe.Pointer(&bin.leftRightPointers[0]))
	spacer.Ambi_bin_process(bin.decoder_handle, p_src, p_dst, bin.channelCount, 2, common.FRAME_SIZE)
	bin.StereoBuffer.InterleaveCBuffers(bin.leftBuffer, bin.rightBuffer)
}
