package policies

import (
	"log"
	"math"
	"sync/atomic"
	"time"

	"github.com/zhangjyr/go-wrk/loader"

	"github.com/zhangjyr/yingwu/gs"
)

func Multiphase(duration int, concurrency int, healthy int, function string, stats *Stats) ([]*loader.LoadCfg, error) {
	// Estimated max pods
	poolNum := int(math.Ceil(float64(concurrency) / float64(healthy)))

	// Start initial pod
	log.Printf("Launching hero fe...")
	fe0, err := gs.ManagerInstance().Create(function, true)
	if err != nil {
		return nil, err
	}

	// Start pods for sharing
	log.Printf("Launching wingman fes, %d in total...", poolNum - 1)
	fe1s, err := gs.ManagerInstance().CreateN("bye", poolNum - 1, true)
	if err != nil {
		gs.ManagerInstance().Clean()
		return nil, err
	}

	// Prepare stats collector
	loaders := make([]*loader.LoadCfg, poolNum)
	scheduler := gs.NewScheduler()
	start := time.Now()
	var queueing int32
	var sharing int32

	// Burst start here
	log.Printf("Incoming burst, concurrency: %d.", concurrency)
	loaders[0] = scheduler.Send(fe0, function, duration, healthy, stats.Aggregator, false)
	atomic.AddInt32(&stats.totalRoutings, int32(healthy))

	if poolNum > 1 {
		// Phase 1: Scale up. Hero holds.
		log.Printf("Scaling up, hero holds %d more connections.", healthy)
		loaders[1] = scheduler.Send(fe0, function, duration, healthy, stats.Aggregator, false)
		atomic.AddInt32(&stats.totalRoutings, int32(healthy))
	}

	if poolNum > 2 {
		// Burst stage in gs
		atomic.StoreInt32(&queueing, int32(concurrency - 2 * healthy))
		log.Printf("Quening %d connections.", queueing)
		for i := 2; i < poolNum; i++ {
			go func(i int) {
				loaders[i] = scheduler.Send(fe1s[i - 1], function, duration, healthy, stats.Aggregator, true)
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

				var remain int32
				load := loaders[i]
				load.Stop()
				if load.IsHold() {
					load.Resume()
					remain = atomic.AddInt32(&queueing, int32(-healthy))
				}
				loaders[i] = scheduler.Send(fe2, function, durationLeft(duration, start), healthy, stats.Aggregator, false)
				atomic.AddInt32(&stats.totalRoutings, int32(healthy))
				shared := atomic.AddInt32(&sharing, -1)
				if remain > 0 {
					log.Printf("Reinforcement arrives(%s), dealing %d connection, %d remaining in queue.", elapsed(start), healthy, remain)
				} else if shared >= 0 {
					log.Printf("Reinforcement arrives(%s), dealing %d connection, %d fes left sharing", elapsed(start), healthy, shared)
				} else {
					log.Printf("Reinforcement arrives(%s), dealing %d connection", elapsed(start), healthy)
				}
			}(i)
		}

		// Start sharing
		log.Printf("Summoning wingman fes, %d in total...", poolNum - 1)
		for i := 1; i < poolNum; i++ {
			go func(i int) {
				err := scheduler.Share(fe1s[i - 1], function)
				if err != nil {
					log.Printf(err.Error())
					return
				}

				var remain int32
				shared := atomic.AddInt32(&sharing, 1)
				load := loaders[i]
				if load.IsHold() {
					load.Resume()
					remain = atomic.AddInt32(&queueing, int32(-healthy))
				} else {
					load.Stop()
					loaders[i] = scheduler.Send(fe1s[i - 1], function, durationLeft(duration, start), healthy, stats.Aggregator, false)
					atomic.AddInt32(&stats.totalRoutings, int32(healthy))
					remain = atomic.LoadInt32(&queueing)
				}
				log.Printf("Wingman arrives(%s), dealing %d connection, %d remaining in queue, %d fes are sharing.",
					elapsed(start), healthy, remain, shared)
			}(i)
		}
	}

	return loaders, nil
}
