package main

import (
	"flag"
//	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
//	"time"

//	"k8s.io/apimachinery/pkg/api/errors"
//	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/zhangjyr/yingwu/gs"
)

var duration int = 10 //seconds
var goroutines int = 2
var kubeconfig *string

func init() {
	flag.IntVar(&goroutines, "c", 10, "Number of goroutines to use (concurrent connections)")
	flag.IntVar(&duration, "d", 10, "Duration of test in seconds")
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU() * 2)
	flag.Parse()

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
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

	_, err = gs.ManagerInstance().Create("hello", true)
	if err != nil {
		log.Println(err.Error())
	}

	gs.NewScheduler().Send(duration, goroutines)

	gs.ManagerInstance().Clean()



	// pod2s := podmgr.start("bye", 8)
	// go gs.load(pod1o, "hello", 100000)
	// pod1s := pod.mgr.start("hello", 8)
	// pod2s.share("hello")
	// go gs.share(pod2, "hello")
	// go gs.add(<-pod1s, "hello")
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
