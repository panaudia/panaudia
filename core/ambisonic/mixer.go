package ambisonic

import (
	"fmt"
	"unsafe"

	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/spacer"
	"gonum.org/v1/gonum/blas"
	"gonum.org/v1/gonum/blas/blas32"
)

type Mixer struct {
	inputTotalCount   int
	inputRunningCount int
	mixerConfig       common.MixerConfig

	//packed versions recreated each pass
	packedInputs          []float32
	packedWeights         []float32
	previousPackedWeights []float32

	//temp premixes
	tempMix  []float32
	MixCount int
}

func NewMixer(mixerConfig common.MixerConfig) *Mixer {

	mixer := Mixer{mixerConfig: mixerConfig,
		inputTotalCount: 0, inputRunningCount: 0}

	inputsSize := mixerConfig.FrameSize * mixerConfig.MaxNodes
	weightsSize := mixerConfig.ChannelCount * mixerConfig.MaxNodes
	outputSize := mixerConfig.ChannelCount * mixerConfig.FrameSize

	//Weights get packed transposed into these when you add an input
	mixer.packedInputs = make([]float32, inputsSize)
	mixer.packedWeights = make([]float32, weightsSize)
	mixer.previousPackedWeights = make([]float32, weightsSize)

	mixer.tempMix = make([]float32, outputSize)

	return &mixer
}

func (mixer *Mixer) Reset(count int) {
	mixer.packedInputs = mixer.packedInputs[:0]
	mixer.packedWeights = mixer.packedWeights[:count*mixer.mixerConfig.ChannelCount]
	mixer.previousPackedWeights = mixer.previousPackedWeights[:count*mixer.mixerConfig.ChannelCount]
	mixer.inputTotalCount = count
	mixer.inputRunningCount = 0
}

func (mixer *Mixer) AddInput(input []float32, weights []float32, previousWeights []float32) {
	mixer.packedInputs = append(mixer.packedInputs, input...)
	for i := range weights {
		transposeIndex := (i * mixer.inputTotalCount) + mixer.inputRunningCount
		mixer.packedWeights[transposeIndex] = weights[i]
		mixer.previousPackedWeights[transposeIndex] = previousWeights[i]
	}
	mixer.inputRunningCount++
}

func (mixer *Mixer) PrintState() {
	fmt.Println("input count:", mixer.inputTotalCount)
	fmt.Println("packed inputs:", mixer.packedInputs)
	fmt.Println("packed weights:", mixer.packedWeights)
	fmt.Println("previous packed weights:", mixer.previousPackedWeights)
}

func (mixer *Mixer) Mix(output []float32) {

	spacer.Panaudia_utils_internal_encode(mixer.inputTotalCount,
		mixer.mixerConfig.ChannelCount,
		mixer.inputTotalCount,
		mixer.mixerConfig.FrameSize,
		uintptr(unsafe.Pointer(&mixer.packedInputs[0])),
		uintptr(unsafe.Pointer(&mixer.packedWeights[0])),
		uintptr(unsafe.Pointer(&mixer.previousPackedWeights[0])),
		uintptr(unsafe.Pointer(&output[0])),
		uintptr(unsafe.Pointer(&mixer.tempMix[0])))

	mixer.MixCount += mixer.inputTotalCount
}

func (mixer *Mixer) Mix2(output []float32) {

	AmbisonicEncode(mixer.inputTotalCount,
		mixer.mixerConfig.ChannelCount,
		mixer.inputTotalCount,
		mixer.mixerConfig.FrameSize,
		mixer.packedInputs,
		mixer.previousPackedWeights,
		output)

	AmbisonicEncode(mixer.inputTotalCount,
		mixer.mixerConfig.ChannelCount,
		mixer.inputTotalCount,
		mixer.mixerConfig.FrameSize,
		mixer.packedInputs,
		mixer.packedWeights,
		mixer.tempMix)

	var v float32
	var index int
	div := float32(mixer.mixerConfig.FrameSize - 1)
	// Fused Fade Operation:
	for i := 0; i < mixer.mixerConfig.ChannelCount; i++ {
		for j := 0; j < mixer.mixerConfig.FrameSize; j++ {
			v = float32(j) / div
			index = (i * mixer.mixerConfig.FrameSize) + j
			output[index] = v*mixer.tempMix[index] + ((1.0 - v) * output[index])
		}
	}
}

func AmbisonicEncode(nInputs int,
	nOutputs int,
	nMaxInputs int,
	nSamples int,
	inputs []float32,
	weights []float32,
	outputs []float32) {

	// Ensure the sizes of weights and inputs match the expected dimensions
	// inputs:  [nInputs, nSamples]
	// weights: [nOutputs, nMaxInputs]
	// outputs: [nOutputs, nSamples]

	// Create BLAS-compatible matrices
	weightsMatrix := blas32.General{
		Rows:   nOutputs,
		Cols:   nMaxInputs,
		Data:   weights,
		Stride: nMaxInputs,
	}
	inputMatrix := blas32.General{
		Rows:   nInputs,
		Cols:   nSamples,
		Data:   inputs,
		Stride: nSamples,
	}
	outputMatrix := blas32.General{
		Rows:   nOutputs,
		Cols:   nSamples,
		Data:   outputs,
		Stride: nSamples,
	}

	//fmt.Printf("Gemm in go")

	// Perform the matrix multiplication using sgemm equivalent in Go
	// Float32 version of GEMM (Matrix-Matrix Multiplication):
	//
	// cblas_sgemm( CblasRowMajor,
	//              NoTrans, NoTrans,
	//              nOutputs, nSamples, nInputs,
	//              1.0f,
	//              weights, nMaxInputs,
	//              inputs,  nSamples,
	//              0.0f,
	//              outputs, nSamples );
	//
	// In Go: Gemm(settings)
	blas32.Gemm(blas.NoTrans, blas.NoTrans, 1.0,
		weightsMatrix, inputMatrix, 0.0, outputMatrix)
}
