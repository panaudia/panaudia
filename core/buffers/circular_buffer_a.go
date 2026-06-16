package buffers

import (
	"github.com/panaudia/panaudia/core/common"
)

//type BufferStatsDelegate interface {
//	NotifyReadMiss(miss int)
//	NotifySessionGone(sessionId uint64)
//}

type CircularBufferA struct {
	length          int
	writeHead       int
	readHead        int
	minBehind       int
	maxBehind       int
	data            []float32
	zero            []float32
	zone            int
	statsDelegate   BufferStatsDelegate
	totalCorrection int
	readCount       int
	//lastPrintCorrection  int
}

func NewCircularBufferA(length int, minBehind int, maxBehind int, statsDelegate BufferStatsDelegate) *CircularBufferA {
	buffer := CircularBufferA{}
	buffer.length = length
	buffer.minBehind = minBehind
	buffer.maxBehind = maxBehind
	buffer.writeHead = 0
	buffer.readHead = 0
	buffer.zone = 100
	buffer.data = make([]float32, length)
	buffer.zero = make([]float32, length)
	buffer.statsDelegate = statsDelegate
	buffer.totalCorrection = 0
	return &buffer
}

func (buffer *CircularBufferA) Write(src []float32) {

	//common.LogDebug("Write in CircularBuffer")

	n := len(src)

	remain := buffer.length - buffer.writeHead
	if n <= remain {
		copy(buffer.data[buffer.writeHead:buffer.writeHead+n], src[0:n])
		buffer.writeHead = buffer.writeHead + n
		if buffer.writeHead == buffer.length {
			buffer.writeHead = 0
		}
	} else {
		copy(buffer.data[buffer.writeHead:buffer.writeHead+remain], src[0:remain])
		copy(buffer.data[0:n-remain], src[remain:n])
		buffer.writeHead = n - remain
	}
	// fmt.Printf("circle: %v\n", buffer.data)
}

func (buffer *CircularBufferA) Read(dst []float32) bool {

	//common.LogDebug("Read in CircularBuffer")

	behind := buffer.writeHead - buffer.readHead

	if behind < 0 {
		behind = behind + buffer.length
	}

	buffer.readCount++

	if behind < buffer.minBehind || behind > buffer.maxBehind {
		//fmt.Printf("behind fail: %d\n", behind)
		copy(dst, buffer.zero)
		if buffer.statsDelegate != nil {
			if behind < buffer.minBehind {
				buffer.statsDelegate.NotifyReadMiss(1)
			}
			if behind > buffer.maxBehind {
				buffer.statsDelegate.NotifyReadMiss(2)
			}
		}
		return false
	}

	headCorrection := 0

	if behind < buffer.minBehind+buffer.zone {
		if behind < buffer.minBehind+(buffer.zone/4) {
			headCorrection -= 4
		} else {
			if behind < buffer.minBehind+(buffer.zone/2) {
				headCorrection -= 2
			} else {
				headCorrection -= 1
			}
		}

	} else {

		if behind > buffer.maxBehind-buffer.zone {
			if behind > buffer.maxBehind-(buffer.zone/4) {
				headCorrection += 4
			} else {
				if behind > buffer.maxBehind-(buffer.zone/2) {
					headCorrection += 2
				} else {
					headCorrection += 1
				}
			}
			common.LogVerbose("totalCorrection: %d", buffer.totalCorrection)
			common.LogVerbose("headCorrection: %d", headCorrection)
		}
	}

	buffer.totalCorrection += headCorrection

	n := len(dst)
	remain := buffer.length - buffer.readHead
	if n <= remain {
		copy(dst, buffer.data[buffer.readHead:])
		buffer.readHead = buffer.readHead + n + headCorrection
	} else {
		copy(dst[0:remain], buffer.data[buffer.readHead:buffer.readHead+remain])
		copy(dst[remain:n], buffer.data[0:n-remain])
		buffer.readHead = n - remain + headCorrection
	}

	if buffer.readHead >= buffer.length || buffer.readHead < 0 {
		buffer.readHead = 0
	}

	return true
}

// GetStats returns current diagnostic statistics.
func (buffer *CircularBufferA) GetStats() CircularBufferStats {
	behind := buffer.GetBehind()
	fillMs := float64(behind) / 48000.0 * 1000.0

	zone := 0
	if behind < buffer.minBehind {
		zone = -1
	} else if behind > buffer.maxBehind {
		zone = 1
	}

	state := statePlaying
	if behind < buffer.minBehind {
		state = stateFilling
	}

	return CircularBufferStats{
		FillLevelSamples: behind,
		FillLevelMs:      fillMs,
		CurrentZone:      zone,
		State:            state,
	}
}

// GetBehind returns the current behind value (writeHead - readHead) for debugging
func (buffer *CircularBufferA) GetBehind() int {
	behind := buffer.writeHead - buffer.readHead
	if behind < 0 {
		behind = behind + buffer.length
	}
	return behind
}
