package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
//	"path/filepath"
	"runtime"
	"time"

	"k8s.io/client-go/kubernetes"
//	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/rest"
	"github.com/zhangjyr/go-wrk/loader"
	"github.com/zhangjyr/go-wrk/util"

	"github.com/zhangjyr/yingwu/gs"
	"github.com/zhangjyr/yingwu/policies"
)

var duration int    //seconds
var goroutines int
var healthy int
var function string
var datafile string
var policy string
// var kubeconfig *string

func init() {
	flag.IntVar(&duration, "d", 30, "Duration of test in seconds")
	flag.IntVar(&goroutines, "c", 10, "Number of goroutines to use (concurrent connections)")
	flag.IntVar(&healthy, "h", 1, "Number of goroutines to be considered healthy")
	flag.StringVar(&function, "f", "hello", "Function to invoke")
	flag.StringVar(&datafile, "o", "", "Path to output data")
	flag.StringVar(&policy, "p", "", "Policy of mimicking. Available: multiphase, scaleout.")
	// if home := homeDir(); home != "" {
	// 	kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	// } else {
	// 	kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	// }
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU() + goroutines)
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

	// Create the clientset and initialize manager
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	_, err = gs.InitializeManager(clientset)
	if err != nil {
		panic(err.Error())
	}

	// Initialize Stats Aggregator
	stats := policies.NewStats(goroutines)

	// Apply policy
	var loaders []*loader.LoadCfg
	switch policy {
	case "multiphase":
		loaders, err = policies.Multiphase(duration, goroutines, healthy, function, stats)
	case "scaleout":
		loaders, err = policies.Scaleout(duration, goroutines, healthy, function, stats)
	}
	if err != nil {
		panic(err.Error())
	}

	// Wait for end
	wait(sigChan, loaders, stats)

	// Suicide
	log.Println("Expelling yingwu(鹦鹉)...")
	clientset.CoreV1().Pods("hyperfaas").Delete("yingwu", nil)
}

func wait(sigChan chan os.Signal, loaders []*loader.LoadCfg, policyStats *policies.Stats) {
	var responders int32
	aggStats := loader.RequesterStats{MinRequestTime: time.Minute}

	var file *os.File
	var err error
	if len(datafile) > 0 {
		file, err = os.OpenFile(datafile, os.O_CREATE|os.O_WRONLY, 0660)
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
		case stats := <-policyStats.Aggregator:
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

			if responders == policyStats.TotalRoutings() {
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
