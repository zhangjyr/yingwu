package policies

import (
	"log"
	"math"
	"sync/atomic"
	"time"

	"github.com/zhangjyr/go-wrk/loader"

	"github.com/zhangjyr/yingwu/gs"
)

func Scaleout(duration int, concurrency int, healthy int, function string, stats *Stats) ([]*loader.LoadCfg, error) {
	// Estimated max pods
	poolNum := int(math.Ceil(float64(concurrency) / float64(healthy)))

	// Start initial pod
	log.Printf("Launching hero fe...")
	fe0, err := gs.ManagerInstance().Create(function, true)
	if err != nil {
		return nil, err
	}

	loaders := make([]*loader.LoadCfg, poolNum)
	scheduler := gs.NewScheduler()
	start := time.Now()

	// Burst start here
	log.Printf("Incoming burst, concurrency: %d.", concurrency)

	// Burst stage in original pod
	for i := 0; i < poolNum; i++ {
		go func(i int) {
			loaders[i] = scheduler.Send(fe0, function, duration, healthy, stats.Aggregator, false)
			atomic.AddInt32(&stats.totalRoutings, int32(healthy))
		}(i)
	}

	// Start new pods
	log.Printf("Launching reinforcement fes, %d in total...", poolNum - 1)
	for i := 1; i < poolNum; i++ {
		go func(i int) {
			fe2, err := gs.ManagerInstance().Create(function, true)
			if err != nil {
				log.Printf(err.Error())
				return
			}

			load := loaders[i]
			load.Stop()
			loaders[i] = scheduler.Send(fe2, function, durationLeft(duration, start), healthy, stats.Aggregator, false)
			atomic.AddInt32(&stats.totalRoutings, int32(healthy))
			log.Printf("Reinforcement arrives(%s), dealing %d connection", elapsed(start), healthy)
		}(i)
	}

	return loaders, nil
}
