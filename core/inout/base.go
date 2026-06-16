package inout

type TickSource interface {
	GetTick() int64
}

type MonoInput interface {
	ReadMono(dst []float32)
	GetTick() int64
	BeforeDestroy()
}

type AmbisonicOutput interface {
	WriteAmbisonic(ambisonicChannels []float32)
	BeforeDestroy()
}
