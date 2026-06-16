package space

import (
	"sync"

	"github.com/panaudia/panaudia/core/ambisonic"
	"github.com/panaudia/panaudia/core/common"
)

type WorkerQueued struct {
	run         bool
	mixer       *ambisonic.Mixer
	reverbMixer *ambisonic.Mixer
	JobQueue    chan Job
}

func (worker *WorkerQueued) GetMixCount() int {
	return worker.mixer.MixCount
}

func (worker *WorkerQueued) ResetMixCount() {
	worker.mixer.MixCount = 0
}

func NewWorkerQueued(wg *sync.WaitGroup, mixerConfig common.MixerConfig) *WorkerQueued {
	reverbMixerConfig := mixerConfig
	reverbMixerConfig.ChannelCount = common.REVERB_CHANNELS
	reverbMixerConfig.Order = common.OrderForChannelCount(common.REVERB_CHANNELS)

	worker := WorkerQueued{run: true, mixer: ambisonic.NewMixer(mixerConfig), reverbMixer: ambisonic.NewMixer(reverbMixerConfig)}
	worker.run = true
	worker.JobQueue = make(chan Job, 400)
	go func() {
		for job := range worker.JobQueue {
			if job.Target != nil {
				job.Target.DoCommand(job.Command, worker.mixer, worker.reverbMixer)
			}
			wg.Done()
		}
	}()
	return &worker
}
