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
	"github.com/tsliwowicz/go-wrk/loader"
	"github.com/tsliwowicz/go-wrk/util"

	"github.com/zhangjyr/yingwu/gs"
)

var duration int    //seconds
var goroutines int
var healthy int
var function *string
// var kubeconfig *string

func init() {
	flag.IntVar(&duration, "d", 30, "Duration of test in seconds")
	flag.IntVar(&goroutines, "c", 10, "Number of goroutines to use (concurrent connections)")
	flag.IntVar(&healthy, "h", 1, "Number of goroutines to be considered healthy")
	function = flag.String("function", "hello", "Function to invoke")
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

	log.Printf("Launching main function environment...")
	fe0, err := gs.ManagerInstance().Create(*function, true)
	if err != nil {
		panic(err.Error())
		return
	}

	warmNum := int(math.Ceil(float64(goroutines) / float64(healthy))) - 1
	log.Printf("Launching other function environments, %d in total...", warmNum)
	fe1s, err := gs.ManagerInstance().CreateN("bye", warmNum, true)
	if err != nil {
		gs.ManagerInstance().Clean()
		panic(err.Error())
		return
	}

	statsAggregator := make(chan *loader.RequesterStats, goroutines * 2)	// total goroutines < goroutines * 2
	loaders := make([]*loader.LoadCfg, 1 + warmNum)
	scheduler := gs.NewScheduler()
	start := time.Now()
	var totalRoutings int32

	// Burst start
	loaders[0] = scheduler.Send(fe0, *function, duration, healthy, statsAggregator)
	totalRoutings += 2

	// Start new pods
	for i := 0; i < warmNum; i++ {
		go func(i int) {
			fe2, err := gs.ManagerInstance().Create(*function, true)
			if err != nil {
				log.Printf(err.Error())
				return
			}

			if loaders[i + 1] != nil {
				loaders[i + 1].Stop()
			}
			loaders[i + 1] = scheduler.Send(fe2, *function, durationLeft(duration, start), healthy, statsAggregator)
			atomic.AddInt32(&totalRoutings, int32(healthy))
		}(i)
	}

	// Start sharing
	for i := 0; i < warmNum; i++ {
		go func(i int) {
			err := scheduler.Share(fe1s[i], *function)
			if err != nil {
				log.Printf(err.Error())
				return
			}

			if loaders[i + 1] != nil {
				// New pod started
				return
			}

			loaders[i + 1] = scheduler.Send(fe1s[i], *function, durationLeft(duration, start), healthy, statsAggregator)
			atomic.AddInt32(&totalRoutings, int32(healthy))
		}(i)
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

	log.Println("Removing yingwu...")
	clientset.CoreV1().Pods("hyperfaas").Delete("yingwu", nil)
}

func durationLeft(duration int, started time.Time) int {
	return int(math.Round(float64(duration) - time.Since(started).Seconds()))
}

func wait(sigChan chan os.Signal, statsAggregator chan *loader.RequesterStats, loaders []*loader.LoadCfg, totalRoutings *int32) {
	var responders int32
	aggStats := loader.RequesterStats{MinRequestTime: time.Minute}
	// for responders < totalRoutings {
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
