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
		return nil, err
	}

	// for i, fe := range fe1s {
	// 	err := scheduler.Share(fe1s[i - 1], function)
	// 	if err != nil {
	// 		return nil, err
	// 	}
	// }

	// Prepare stats collector
	loaders := make([]*loader.LoadCfg, poolNum)
	waitLoads := make([]chan struct{}, poolNum)
	for i := 1; i < poolNum; i++ {
		waitLoads[i] = make(chan struct{})
	}
	scheduler := gs.NewScheduler()
	stats.Start = time.Now()
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
				close(waitLoads[i])
			}(i)
		}

		// Start sharing
		log.Printf("Summoning wingman fes, %d in total...", poolNum - 1)
		for i := 1; i < poolNum; i++ {
			go func(i int) {
				err := scheduler.Swap(fe1s[i - 1], function)
				if err != nil {
					log.Printf(err.Error())
					return
				}

				var remain int32
				shared := atomic.AddInt32(&sharing, 1)

				<- waitLoads[i]
				load := loaders[i]
				if load.IsHold() {
					load.Resume()
					remain = atomic.AddInt32(&queueing, int32(-healthy))
				} else {
					load.Stop()
					loaders[i] = scheduler.Send(fe1s[i - 1], function, durationLeft(duration, stats.Start), healthy, stats.Aggregator, false)
					atomic.AddInt32(&stats.totalRoutings, int32(healthy))
					remain = atomic.LoadInt32(&queueing)
				}
				log.Printf("Wingman arrives(%s), dealing %d connection, %d remaining in queue, %d fes are sharing.",
					elapsed(stats.Start), healthy, remain, shared)
			}(i)
		}

		// // Start new pods
		// log.Printf("Launching reinforcement fes, %d in total...", poolNum - 1)
		// for i := 1; i < poolNum; i++ {
		// 	go func(i int) {
		// 		fe2, err := gs.ManagerInstance().Create(function, true)
		// 		if err != nil {
		// 			log.Printf(err.Error())
		// 			return
		// 		}
		//
		// 		var remain int32
		// 		load := loaders[i]
		// 		load.Stop()
		// 		if load.IsHold() {
		// 			load.Resume()
		// 			remain = atomic.AddInt32(&queueing, int32(-healthy))
		// 		}
		// 		loaders[i] = scheduler.Send(fe2, function, durationLeft(duration, stats.Start), healthy, stats.Aggregator, false)
		// 		atomic.AddInt32(&stats.totalRoutings, int32(healthy))
		// 		shared := atomic.AddInt32(&sharing, -1)
		// 		if remain > 0 {
		// 			log.Printf("Reinforcement arrives(%s), dealing %d connection, %d remaining in queue.", elapsed(stats.Start), healthy, remain)
		// 		} else if shared >= 0 {
		// 			log.Printf("Reinforcement arrives(%s), dealing %d connection, %d fes left sharing", elapsed(stats.Start), healthy, shared)
		// 		} else {
		// 			log.Printf("Reinforcement arrives(%s), dealing %d connection", elapsed(stats.Start), healthy)
		// 		}
		// 	}(i)
		// }
	}

	return loaders, nil
}

func multiphase(duration int, scheduler *gs.Scheduler, meta *CompositionMeta, schedule *OchestratorSchedule, fes []*gs.FE, stats *Stats) (error) {
	// Burst start here
	if schedule.Burst > 0 {
		log.Printf("Incoming burst, concurrency: %d.", schedule.Concurrency)
	}

	// Find standbys
	standbys := make(map[string][]int)
	scaleUpdates := 0
	for pod, update := range schedule.Updates {
		if len(update.Function) > 0 {
			scaleUpdates ++
			continue
		}

		// Buffers can be scaled up or throttle at this stage
		if fes[pod] != nil && !fes[pod].IsBuffer() {
			// FE already being scaled up can not scale up again
			if fes[pod].Loader.Threshhold() <= int32(schedule.States[pod].Conn) {
				function := schedule.States[pod].Function
				standby, seen := standbys[function]
				if !seen {
					standby = make([]int, 0, len(schedule.Updates))
				}
				standbys[function] = append(standby, pod)
			}

			if update.Conn >= 0 {
				fes[pod].Loader.Throttle(update.Conn)
			}
		}
	}

	// All done
	if scaleUpdates == 0 {
		return nil
	}

	// Scale
	standbyCounts := make(map[string]int, len(standbys))
	for function, standby := range standbys {
		standbyCounts[function] = len(standby)
	}
	for pod, update := range schedule.Updates {
		if len(update.Function) == 0 {
			continue
		}

		// Phase 1: Scale up. Hero holds.
		var scaleUpFe *gs.FE
		standby, seen := standbys[update.Function]
		if seen && len(standby) > 0 {
			scaleUpFe = fes[standby[0]]
			standbys[update.Function] = standby[1:]

			log.Printf("Scaling up, hero holds %d more connections.", update.Conn)
			scaleUpFe.Loader.ThrottleUp(update.Conn)

			// There are enough standby pods, skip scaling out
			if update.Burst <= standbyCounts[update.Function] {
				continue
			}

			log.Printf("Calling reinforcement for scaled up.")
		} else {
			queueing := scheduler.Enqueue(int32(update.Conn))
			log.Printf("Calling reinforcement, %d remaining in queue, %d fes are sharing.", queueing, scheduler.Sharing())
		}

		// Phase 2: Quick Switch.
		if fes[pod] != nil {
			// Switch
			go func(pod int, update *OSStates, scaleUpFe *gs.FE) {
				// Hold and unthrottle, Time counting.
				fes[pod].Loader.Hold()
				if scaleUpFe == nil {
					// Only start count time if no scaleup
					fes[pod].Loader.Throttle(update.Conn)
				}

				err := scheduler.Swap(fes[pod], update.Function)
				if err != nil {
					log.Printf(err.Error())
					return
				}

				if scaleUpFe != nil {
					// This holds even after burst.
					err := scaleUpFe.Loader.ThrottleDown(update.Conn)
					// Possibilities are burst passed
					if err == nil {
						fes[pod].Loader.Throttle(update.Conn)
					}
				} else {
					scheduler.Dequeue(int32(update.Conn))
				}
				// This holds even after burst.
				fes[pod].Loader.Resume()
				log.Printf("Close reinforcement arrives(%s), dealing %d connection, %d remaining in queue, %d fes are sharing.",
					elapsed(stats.Start), update.Conn, scheduler.Queueing(), scheduler.Sharing())
			}(pod, update, scaleUpFe)
		} else {
			if scaleUpFe != nil {
				fes[pod] = scaleUpFe.NewBuffer()
			}

			// Phase 3: Start new pods
			go func(pod int, update *OSStates, scaleUpFe *gs.FE) {
				fe, err := gs.ManagerInstance().Create(update.Function, true)
				if err != nil {
					log.Printf(err.Error())
					return
				}

				if fes[pod] != nil {
					fes[pod].Loader.ThrottleDown(update.Conn)
					if fes[pod] != scaleUpFe {
						scheduler.Unshare(fes[pod])
					} else {
						scheduler.Dequeue(int32(update.Conn))
					}
				}

				scheduler.SendWithConfig(fe, &gs.SendConfig{
					update.Function,
					durationLeft(duration, stats.Start),
					update.Conn,
					meta.MaxCPP * 2,
					false,
				}, stats.Aggregator)
				atomic.AddInt32(&stats.totalRoutings, int32(meta.MaxCPP * 2))

				log.Printf("Reinforcement arrives(%s), dealing %d connection, %d remaining in queue, %d fes are sharing.",
					elapsed(stats.Start), update.Conn, scheduler.Queueing(), scheduler.Sharing())
			}(pod, update, scaleUpFe)

			// Phase 2: Sharing Switch.
			sharedFe, err := scheduler.GetShareable()
			if err != nil {
				log.Printf(err.Error())
				continue
			}
			if scaleUpFe == nil {
				sharedFe.Loader.Hold()
				sharedFe.Loader.Throttle(update.Conn)
			}

			log.Printf("Summoning wingman fes...")
			go func(pod int, update *OSStates, sharedFe *gs.FE) {
				err := scheduler.Share(sharedFe, update.Function)
				if err != nil {
					log.Printf(err.Error())
					return
				}

				if fes[pod] == nil {
					scheduler.Dequeue(int32(update.Conn))
					sharedFe.Loader.Resume()
				} else if !fes[pod].IsBuffer() {
					// New pod already started
					scheduler.Unshare(sharedFe)
					sharedFe.Loader.Throttle(update.Conn)
					sharedFe.Loader.Resume()
					log.Printf("Wingman arrives(%s), but too late, %d remaining in queue, %d fes are sharing.",
						elapsed(stats.Start), scheduler.Queueing(), scheduler.Sharing())
					return
				} else {
					fes[pod].Loader.ThrottleDown(update.Conn)
					sharedFe.Loader.Throttle(update.Conn)
				}

				fes[pod] = sharedFe.NewBuffer()
				log.Printf("Wingman arrives(%s), dealing %d connection, %d remaining in queue, %d fes are sharing.",
					elapsed(stats.Start), update.Conn, scheduler.Queueing(), scheduler.Sharing())
			}(pod, update, sharedFe)
		}
	}

	return nil
}
