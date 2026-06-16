package roc

type Demuxerf32 interface {
	WriteDemuxf32(src []float32) error
}
