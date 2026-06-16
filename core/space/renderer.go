package space

import (
	"sync"

	"github.com/panaudia/panaudia/core/common"
	// "fmt"
)

type IRenderer interface {
	Render(space *BaseSpace)
}

type Renderer struct {
	workers     []*WorkerQueued
	workerIndex int
}

func NewRenderer(count int, wg *sync.WaitGroup, mixerConfig common.MixerConfig) *Renderer {
	renderer := Renderer{}
	renderer.workers = make([]*WorkerQueued, count*2)
	renderer.workerIndex = 0
	for i := 0; i < count*2; i++ {
		worker := NewWorkerQueued(wg, mixerConfig)
		renderer.workers[i] = worker
	}
	return &renderer
}

func (renderer *Renderer) NextWorker() *WorkerQueued {
	worker := renderer.workers[renderer.workerIndex]
	renderer.workerIndex = (renderer.workerIndex + 1) % len(renderer.workers)
	return worker
}

func (renderer *Renderer) Render(space *BaseSpace) {

	renderer.resetMixCount()

	renderer.In(space)
	space.CollectGlobals()
	renderer.Across(space)
	renderer.Out(space)

	//mixes := renderer.GetMixCount()
	////
	//common.LogDebug("Rendered")

}

func (renderer *Renderer) resetMixCount() {
	for _, worker := range renderer.workers {
		worker.ResetMixCount()
	}
}

func (renderer *Renderer) GetMixCount() int {

	count := 0

	for _, worker := range renderer.workers {
		count += worker.GetMixCount()
	}
	return count
}

func (renderer *Renderer) In(space *BaseSpace) {
	for _, node := range space.Nodes {
		space.Wg.Add(1)
		renderer.NextWorker().JobQueue <- Job{Target: node, Command: COMMAND_NODE_IN}
	}
	space.Wg.Wait()
}

func (renderer *Renderer) Across(space *BaseSpace) {

	space.Wg.Add(len(space.Nodes))
	for _, node := range space.Nodes {
		renderer.NextWorker().JobQueue <- Job{Target: node, Command: COMMAND_NODE_ACROSS}
	}
	space.Wg.Wait()
}

func (renderer *Renderer) Out(space *BaseSpace) {

	for _, node := range space.Nodes {
		if node.Output != nil {
			space.Wg.Add(1)
			renderer.NextWorker().JobQueue <- Job{Target: node, Command: COMMAND_NODE_OUT}
		}
	}
	space.Wg.Wait()
}
