package space

import (
	"sync"

	"github.com/panaudia/panaudia/core/ambisonic"
	"github.com/panaudia/panaudia/core/common"
)

type IJobTarget interface {
	DoCommand(command int, mixer *ambisonic.Mixer, reverbMixer *ambisonic.Mixer)
}

type Job struct {
	Target  IJobTarget
	Command int
}

type Worker struct {
	run         bool
	mixer       *ambisonic.Mixer
	reverbMixer *ambisonic.Mixer
}

func NewWorker(jobQueue chan Job, wg *sync.WaitGroup, mixerConfig common.MixerConfig) *Worker {

	reverbMixerConfig := mixerConfig
	reverbMixerConfig.ChannelCount = common.REVERB_CHANNELS
	reverbMixerConfig.Order = common.OrderForChannelCount(common.REVERB_CHANNELS)

	worker := Worker{run: true, mixer: ambisonic.NewMixer(mixerConfig), reverbMixer: ambisonic.NewMixer(reverbMixerConfig)}
	worker.run = true
	go worker.start(jobQueue, wg)
	return &worker
}

func (worker *Worker) start(jobQueue chan Job, wg *sync.WaitGroup) {

	for worker.run {
		job := <-jobQueue
		if job.Target != nil {
			job.Target.DoCommand(job.Command, worker.mixer, worker.reverbMixer)
		}
		wg.Done()
	}
}
