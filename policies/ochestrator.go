package policies

import (
	"fmt"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"sort"
	"strings"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/zhangjyr/go-wrk/loader"

	"github.com/zhangjyr/yingwu/gs"
)

type TickSchedule struct {
	Tick int `json:"tick"`
	Pods []string `json:"pods"`
	Conn int `json:"conn"`
	Policy string `json:"policy"`
}

type CompositionFunction struct {
	Name string `json:"function"`
	Schedule []TickSchedule `json:"schedule"`
}

type CompositionMeta struct {
	Capacity int `json:"capacity"`
	Size int `json:"size"`
	MaxCPP int `json:"maxcpp"`
}

type CompositionConfig struct {
	Meta CompositionMeta `json:"meta"`
	Functions []CompositionFunction `json:"functions"`
}

type OSStates struct {
	Function string
	Conn int
	Burst int
}

func (s *OSStates) Update(o *OSStates) *OSStates {
	if o == nil {
		return s
	}

	update := &OSStates{ "", -1, s.Burst - o.Burst }
	diff := 0
	if o.Function != s.Function {
		diff += 1
		update.Function = s.Function
	}
	if o.Conn != s.Conn {
		diff += 1
		update.Conn = s.Conn
	}

	if diff == 0 {
		// Don't return nil
		// return nil
	}
	return update
}

func (s *OSStates) Delete(burst int) *OSStates {
	return &OSStates{ "", 0, burst - s.Burst }  // Keep function intact and throttle connnection to 0
}

type OchestratorSchedule struct {
	Tick      int
	States    map[int]*OSStates
	Updates   map[int]*OSStates
	Concurrency     int
	Burst           int
}

func MakeSchedules(composition *CompositionConfig) ([]*OchestratorSchedule, error)  {
	// Group by tick.
	ticks := make(map[int][]*CompositionFunction)
	for _, function := range composition.Functions {
		for i, schedule := range function.Schedule {
			tick, seen := ticks[schedule.Tick]
			if !seen {
				tick = make([]*CompositionFunction, 0, len(composition.Functions))
			}

			ticks[schedule.Tick] = append(tick, &CompositionFunction{ function.Name, function.Schedule[i:i+1] })
		}
	}

	// Generate usable schedule
	schedules := make([]*OchestratorSchedule, 0, len(ticks))
	keys := make([]int, 0, len(ticks))
	for tick, _ := range ticks {
		keys = append(keys, tick)
	}
	sort.Ints(keys)

	var last *OchestratorSchedule
	for _, tick := range keys {
		schedule := &OchestratorSchedule{ tick, make(map[int]*OSStates), make(map[int]*OSStates), 0, 0 }
		for _, function := range ticks[tick] {
			connLeft := function.Schedule[0].Conn
			burst := connLeft
			schedule.Concurrency += connLeft
			for _, podDef := range function.Schedule[0].Pods {
				podRange := strings.Split(podDef, "-")

				podStart, err := strconv.Atoi(podRange[0])
				if err != nil {
					return nil, err
				}
				podEnd := podStart
				if len(podRange) >= 2 {
					podEnd, err = strconv.Atoi(podRange[1])
					if err != nil {
						return nil, err
					}
				}

				for pod := podStart; pod < podEnd + 1; pod++ {
					conn := 2
					if connLeft < 2 {
						conn = connLeft
					}
					connLeft -= conn
					states := &OSStates{ function.Name, conn, burst }
					schedule.States[pod] = states
					// Calculate creates and updates
					if last == nil {
						schedule.Updates[pod] = states.Update(nil)
						continue
					}

					update := states.Update(last.States[pod])
					if update != nil {
						schedule.Updates[pod] = update
					}
				}
			}

			// Calculate deletes covered by function spec.
			if last != nil {
				for pod, states := range last.States {
					if states.Function == function.Name && schedule.States[pod] == nil {
						schedule.States[pod] = &OSStates{ states.Function, 0, burst }
						schedule.Updates[pod] = states.Delete(burst)
					}
				}
			}
		}

		// Copy intact states
		if last != nil {
			schedule.Burst = schedule.Concurrency - last.Concurrency
			for pod, states := range last.States {
				if schedule.States[pod] == nil {
					schedule.States[pod] = states
				}
			}
		}

		// Update last
		last = schedule
		schedules = append(schedules, schedule)
	}

	return schedules, nil
}

func (os *OchestratorSchedule) String() string {
	pods := make([]int, 0, len(os.Updates))
	for pod, _ := range os.Updates {
		pods = append(pods, pod)
	}
	sort.Ints(pods)

	updates := make([]string, 0, len(os.Updates))
	for _, pod := range pods {
		updates = append(updates, fmt.Sprintf("Pod %d: %v", pod, *os.Updates[pod]))
	}
	return fmt.Sprintf("Tick %d: \n%s", os.Tick, strings.Join(updates, "\n"))
}

func Ochestrator(duration int, filename string, stats *Stats) ([]*loader.LoadCfg, error) {
	config, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var composition CompositionConfig
	err = json.Unmarshal(config, &composition)
	if err != nil {
		return nil, err
	}

	schedules, err := MakeSchedules(&composition)
	if err != nil {
		return nil, err
	}

	log.Println("Schedule confirmed:")
	for _, schedule := range schedules {
		log.Println(schedule.String())
	}

	if len(schedules) == 0 {
		return nil, errors.New("No schedule available")
	}

	// Prepare loaders
	fes := make([]*gs.FE, composition.Meta.Capacity)
	scheduler := gs.NewOchestratorScheduler(fes)

	frontier := 0
	if schedules[0].Tick == 0 {
		frontier++
		chanErr := make(chan error, len(schedules[0].Updates))
		for pod, update := range schedules[0].Updates {
			log.Printf("Tick 0, launching heros and wingmen")
			go func(pod int, update *OSStates) {
				fe, err := gs.ManagerInstance().Create(update.Function, true)
				if err != nil {
					chanErr <- err
					return
				}

				scheduler.SendWithConfig(fe, &gs.SendConfig{
					update.Function,
					duration,
					update.Conn,
					composition.Meta.MaxCPP * 2,
					false,
				}, stats.Aggregator)
				atomic.AddInt32(&stats.totalRoutings, int32(composition.Meta.MaxCPP * 2))
				fes[pod] = fe
				chanErr <- nil
			}(pod, update)
		}

		// Wait for all launched
		launched := 0
		for launched < len(schedules[0].Updates) {
			select {
			case err := <-chanErr:
				if err != nil {
					return nil, err
				}
				launched++
			}
		}
	}

	stats.Start = time.Now()
	tick := 0
	chanErr := make(chan error)

	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()
	for frontier < len(schedules) {
		select {
		case <-stats.SigChan:
			// Stop execution
			break
		case err := <-chanErr:
			return nil, err
		case <-timer.C:
		}

		tick++

		if schedules[frontier].Tick <= tick {
			go func() {
				frontier++
				err := multiphase(duration, scheduler, &composition.Meta, schedules[frontier - 1], fes, stats)
				if err != nil {
					chanErr <- err
				}
			}()
		}

		// Stop and drain the timer to be accurate and safe to reset.
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(1 * time.Second)
	}

	loaders := make([]*loader.LoadCfg, stats.TotalRoutings())
	for _, fe := range fes {
		loaders = append(loaders, fe.Loader)
	}

	return loaders, nil
}
