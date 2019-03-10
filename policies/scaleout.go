package policies

import (
	"log"
	"math"
	"sync/atomic"
	"time"

	"github.com/zhangjyr/go-wrk/loader"

	"github.com/zhangjyr/yingwu/gs"
)

const (
	REBALANCE_INTERVAL = 500 * time.Millisecond
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
	fes := make([]*gs.FE, poolNum)
	loadMap := make([]*gs.FE, poolNum)
	fes[0] = fe0
	scheduler := gs.NewScheduler()
	stats.Start = time.Now()

	// Burst start here
	log.Printf("Incoming burst, concurrency: %d.", concurrency)

	// Burst stage in original pod
	for i := 0; i < poolNum; i++ {
		loaders[i] = scheduler.Send(fe0, function, duration, healthy, stats.Aggregator, false)
		loadMap[i] = fe0
		atomic.AddInt32(&stats.totalRoutings, int32(healthy))
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

			fes[i] = fe2
			log.Printf("Reinforcement arrives(%s), waiting for rebalancing", elapsed(stats.Start))
		}(i)
	}

	timer := time.NewTimer(REBALANCE_INTERVAL)
	defer timer.Stop()
	allReady := false
	for !allReady {
		select {
		case <-stats.SigChan:
			// Stop execution
			break
		// case err := <-chanErr:
		// 	return nil, err
		case <-timer.C:
		}

		// Rebalance
		tests := 0
		rebalanced := 0
		nextTest := 0
		for i, _ := range loaders {
			for j := nextTest; j < poolNum + nextTest; j++ {
				k := j % poolNum
				tests++
				if fes[k] == nil {
					continue
				}

				if loadMap[i] != fes[k] {
					loaders[i].Stop()
					loaders[i] = scheduler.Send(fes[k], function, durationLeft(duration, stats.Start), healthy, stats.Aggregator, false)
					loadMap[i] = fes[k]
					atomic.AddInt32(&stats.totalRoutings, int32(healthy))
					rebalanced++
				}
				nextTest = (k + 1) % poolNum
				break
			}
		}
		if tests == poolNum {
			allReady = true
		}
		if rebalanced > 0 {
			log.Printf("%d connections rebalanced", rebalanced * healthy)
		}

		// Stop and drain the timer to be accurate and safe to reset.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(REBALANCE_INTERVAL)
	}

	return loaders, nil
}
