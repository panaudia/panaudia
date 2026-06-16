package binaural

import (
	"github.com/panaudia/panaudia/core/common"
	"sync"
)

type BinauralDecoderPool struct {
	pool         []*BinauralDecoder
	channelCount int
	sync.Mutex
}

func NewBinauralDecoderPool(size int, channelCount int) *BinauralDecoderPool {
	bdp := BinauralDecoderPool{}
	bdp.channelCount = channelCount
	// Build the full set up front — one dedicated decoder per possible
	// output (the space's max-sources cap). Binaural decoders are expensive to create
	// (SAF HRIR initCodec), so building one lazily mid-session — even a
	// single one on a new connection — risks an audible glitch in the audio
	// already being rendered (CPU spike / pool-lock contention). We pay the
	// whole cost once at startup, before any audio flows; at runtime
	// GetDecoder only claims/Releases from this fixed set, never creating or
	// swapping decoders.
	bdp.pool = make([]*BinauralDecoder, size)
	for i := 0; i < size; i++ {
		bdp.pool[i] = NewBinauralDecoder(channelCount, common.Rotation{})
	}
	return &bdp
}

func (bdp *BinauralDecoderPool) GetDecoder(rotation common.Rotation) *BinauralDecoder {
	bdp.Lock()
	defer bdp.Unlock()

	// All decoders are built up front (see NewBinauralDecoderPool); just
	// claim the first inactive one. Returns nil only if every decoder is in
	// use, which cannot happen while the space caps outputs at the pool size.
	for _, decoder := range bdp.pool {
		if !decoder.active {
			decoder.claim()
			decoder.UpdateRotation(rotation)
			return decoder
		}
	}
	return nil
}
