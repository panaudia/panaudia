package buffers

import (
	"fmt"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/timing"
	"sync"
)

type InputPool struct {
	dirty          []int64
	buffers        [][]byte
	size           int
	last           int
	count          int
	Done           bool
	sourceDelegate common.UdpNodeIODelegate
	sync.Mutex
}

func NewInputPool(size int, count int, sourceDelegate common.UdpNodeIODelegate) *InputPool {
	pool := InputPool{}
	pool.count = count
	pool.sourceDelegate = sourceDelegate
	pool.last = 0
	pool.buffers = make([][]byte, count)
	pool.dirty = make([]int64, count)
	for i := 0; i < count; i++ {
		pool.buffers[i] = make([]byte, size)
	}

	(&pool).startCleaner()

	return &pool
}

func (pool *InputPool) Next() (int, []byte) {
	pool.Lock()
	defer pool.Unlock()
	var poolIndex int
	for i := 0; i < pool.count; i++ {
		poolIndex = pool.last + i
		if poolIndex > pool.count-1 {
			poolIndex = poolIndex - pool.count
		}
		if pool.dirty[poolIndex] == 0 {
			pool.last = poolIndex
			pool.dirty[poolIndex] = pool.sourceDelegate.GetTick()
			return poolIndex, pool.buffers[poolIndex]
		}
	}
	fmt.Printf("POOL FULL!!!!")
	return -1, nil
}

func (pool *InputPool) ReleaseBuffer(poolIndex int) {
	pool.Lock()
	defer pool.Unlock()
	pool.dirty[poolIndex] = 0
}

func (pool *InputPool) startCleaner() {
	go func() {
		ticker := timing.NewTicker(1000, false)
		for !pool.Done {
			spaceTick := pool.sourceDelegate.GetTick()
			for i := 0; i < pool.count; i++ {
				dirty := pool.dirty[i]
				if dirty > 0 && dirty < (spaceTick-200) {
					common.LogInfo("releasing pool: %d %d", i, dirty)
					pool.ReleaseBuffer(i)
				}
			}
			ticker.Tick()
		}
	}()
}
