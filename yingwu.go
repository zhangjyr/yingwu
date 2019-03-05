package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
//	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"k8s.io/client-go/kubernetes"
//	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/rest"
	"github.com/zhangjyr/go-wrk/loader"
	"github.com/zhangjyr/go-wrk/util"

	"github.com/zhangjyr/yingwu/gs"
)

var duration int    //seconds
var goroutines int
var healthy int
var function *string
var datafile *string
// var kubeconfig *string

func init() {
	flag.IntVar(&duration, "d", 30, "Duration of test in seconds")
	flag.IntVar(&goroutines, "c", 10, "Number of goroutines to use (concurrent connections)")
	flag.IntVar(&healthy, "h", 1, "Number of goroutines to be considered healthy")
	function = flag.String("function", "hello", "Function to invoke")
	datafile = flag.String("data", "", "Path to output data")
	// if home := homeDir(); home != "" {
	// 	kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	// } else {
	// 	kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	// }
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU() * 2)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	signal.Notify(sigChan, os.Kill)
	flag.Parse()
	log.Println("Yingwu(鹦鹉) is mimicking...")

	// use the current context in kubeconfig
	// config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	// if err != nil {
	// 	panic(err.Error())
	// }

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	_, err = gs.InitializeManager(clientset)
	if err != nil {
		panic(err.Error())
	}

	// Estimated max pods
	poolNum := int(math.Ceil(float64(goroutines) / float64(healthy)))

	// Start initial pod
	log.Printf("Launching hero fe...")
	fe0, err := gs.ManagerInstance().Create(*function, true)
	if err != nil {
		panic(err.Error())
		return
	}

	// Start pods for sharing
	log.Printf("Launching wingman fes, %d in total...", poolNum - 1)
	fe1s, err := gs.ManagerInstance().CreateN("bye", poolNum - 1, true)
	if err != nil {
		gs.ManagerInstance().Clean()
		panic(err.Error())
		return
	}

	// Prepare stats collector
	statsAggregator := make(chan *loader.RequesterStats, goroutines * 2)	// total goroutines < goroutines * 2
	loaders := make([]*loader.LoadCfg, poolNum)
	scheduler := gs.NewScheduler()
	start := time.Now()
	var totalRoutings int32
	var queueing int32
	var sharing int32

	// Burst start here
	log.Printf("Incoming burst, concurrency: %d.", goroutines)
	loaders[0] = scheduler.Send(fe0, *function, duration, healthy, statsAggregator, false)
	atomic.AddInt32(&totalRoutings, int32(healthy))

	if poolNum > 1 {
		// Phase 1: Scale up. Hero holds.
		log.Printf("Scaling up, hero holds %d more connections.", healthy)
		loaders[1] = scheduler.Send(fe0, *function, duration, healthy, statsAggregator, false)
		atomic.AddInt32(&totalRoutings, int32(healthy))
	}

	if poolNum > 2 {
		// Burst stage in gs
		atomic.StoreInt32(&queueing, int32(goroutines - 2 * healthy))
		log.Printf("Quening %d connections.", queueing)
		for i := 2; i < poolNum; i++ {
			go func(i int) {
				loaders[i] = scheduler.Send(fe1s[i - 1], *function, durationLeft(duration, start), healthy, statsAggregator, true)
				atomic.AddInt32(&totalRoutings, int32(healthy))
			}(i)
		}

		// Start new pods
		log.Printf("Launching reinforcement fes, %d in total...", poolNum - 1)
		for i := 1; i < poolNum; i++ {
			go func(i int) {
				fe2, err := gs.ManagerInstance().Create(*function, true)
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
				loaders[i] = scheduler.Send(fe2, *function, durationLeft(duration, start), healthy, statsAggregator, false)
				atomic.AddInt32(&totalRoutings, int32(healthy))
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
				err := scheduler.Share(fe1s[i - 1], *function)
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
					loaders[i] = scheduler.Send(fe1s[i - 1], *function, durationLeft(duration, start), healthy, statsAggregator, false)
					atomic.AddInt32(&totalRoutings, int32(healthy))
					remain = atomic.LoadInt32(&queueing)
				}
				log.Printf("Wingman arrives(%s), dealing %d connection, %d remaining in queue, %d fes are sharing.",
					elapsed(start), healthy, remain, shared)
			}(i)
		}
	}

	// Wait for end
	wait(sigChan, statsAggregator, loaders, &totalRoutings)

	// pod1 := podmgr.start("hello", 1)
	// pod2s := podmgr.start("bye", 8)
	// go gs.load(pod1o, "hello", 100000)
	// pod1s := podmgr.start("hello", 8)
	// pod2s.share("hello")
	// go gs.share(pod2, "hello")
	// go gs.add(<-pod1s, "hello")

	log.Println("Expelling yingwu(鹦鹉)...")
	clientset.CoreV1().Pods("hyperfaas").Delete("yingwu", nil)
}

func elapsed(start time.Time) string {
	return fmt.Sprintf("%.3fs", time.Since(start).Seconds())
}

func durationLeft(duration int, started time.Time) int {
	return int(math.Round(float64(duration) - time.Since(started).Seconds()))
}

func wait(sigChan chan os.Signal, statsAggregator chan *loader.RequesterStats, loaders []*loader.LoadCfg, totalRoutings *int32) {
	var responders int32
	aggStats := loader.RequesterStats{MinRequestTime: time.Minute}
	// for responders < totalRoutings {
	var file *os.File
	var err error
	if len(*datafile) > 0 {
		file, err = os.OpenFile(*datafile, os.O_CREATE|os.O_WRONLY, 0660)
		if err != nil {
			log.Printf("Warning: failed to open data file. Error: %s.", err.Error())
		} else {
			defer file.Close()
		}
	}
	for {
		select {
		case <-sigChan:
			fmt.Printf("stopping...\n")
			for _, loadGen := range loaders {
				if loadGen != nil {
					loadGen.Stop()
				}
			}
			gs.ManagerInstance().Clean()
			return
		case stats := <-statsAggregator:
			aggStats.NumErrs += stats.NumErrs
			aggStats.NumRequests += stats.NumRequests
			aggStats.TotRespSize += stats.TotRespSize
			aggStats.TotDuration += stats.TotDuration
			aggStats.MaxRequestTime = util.MaxDuration(aggStats.MaxRequestTime, stats.MaxRequestTime)
			aggStats.MinRequestTime = util.MinDuration(aggStats.MinRequestTime, stats.MinRequestTime)
			responders++
			if file != nil {
				for _, item := range stats.Items {
					file.WriteString(item.String())
					file.WriteString("\n")
				}
			}

			if responders == atomic.LoadInt32(totalRoutings) {
				if aggStats.NumRequests == 0 {
					fmt.Println("Error: No statistics collected / no requests found")
					return
				}

				avgThreadDur := aggStats.TotDuration / time.Duration(responders) //need to average the aggregated duration

				reqRate := float64(aggStats.NumRequests) / avgThreadDur.Seconds()
				avgReqTime := aggStats.TotDuration / time.Duration(aggStats.NumRequests)
				bytesRate := float64(aggStats.TotRespSize) / avgThreadDur.Seconds()
				fmt.Printf("%v requests in %v, %v read\n", aggStats.NumRequests, avgThreadDur, util.ByteSize{float64(aggStats.TotRespSize)})
				fmt.Printf("Requests/sec:\t\t%.2f\nTransfer/sec:\t\t%v\nAvg Req Time:\t\t%v\n", reqRate, util.ByteSize{bytesRate}, avgReqTime)
				fmt.Printf("Fastest Request:\t%v\n", aggStats.MinRequestTime)
				fmt.Printf("Slowest Request:\t%v\n", aggStats.MaxRequestTime)
				fmt.Printf("Number of Errors:\t%v\n", aggStats.NumErrs)
			}
		}
	}
}

// func homeDir() string {
// 	if h := os.Getenv("HOME"); h != "" {
// 		return h
// 	}
// 	return os.Getenv("USERPROFILE") // windows
// }
