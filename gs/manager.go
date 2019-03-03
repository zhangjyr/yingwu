package gs

import (
	"fmt"
	"io/ioutil"
	"log"
	"sync"
	"sync/atomic"

	apiv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"github.com/ghodss/yaml"
	"github.com/google/uuid"
)

var (
	mgr  *manager
)

type FE struct {
	Pod      *apiv1.Pod
	Ready    chan struct{}
}

func (fe *FE) SetReady() {
	select {
	case <- fe.Ready:
		// Already closed
	default:
		close(fe.Ready)
	}
}

func (fe *FE) Addr() string {
	<- fe.Ready
	return fmt.Sprintf("http://%s:8080/", fe.Pod.Status.PodIP)
}

func (fe *FE) AdminAddr() string {
	<- fe.Ready
	return fmt.Sprintf("http://%s:8079/", fe.Pod.Status.PodIP)
}

type manager struct {
	funcs        map[string]map[string]*FE
	fes          map[string]*FE
	client       *kubernetes.Clientset
	watcher      watch.Interface
	cleaned      chan struct{}
	mu           sync.RWMutex
}

func InitializeManager(cs *kubernetes.Clientset) (*manager, error) {
	watcher, err := cs.CoreV1().Pods("hyperfaas").Watch(apiv1.ListOptions{})
	if err != nil {
		return nil, err
	}

	if mgr == nil {
		mgr = &manager{
			funcs: make(map[string]map[string]*FE),
			fes: make(map[string]*FE),
			client: cs,
			watcher: watcher,
			cleaned: make(chan struct{}),
		}
	}

	go func() {
		for {
			select {
			case <- mgr.cleaned:
				watcher.Stop()
			case event := <- watcher.ResultChan():
				switch event.Type {
				case watch.Modified:
					pod := event.Object.(*apiv1.Pod)
					fe := mgr.fe(pod.ObjectMeta.Name)
					if fe != nil {
						fe.Pod = pod
						if mgr.IsReadyPod(pod) {
							fe.SetReady()
						}
					}
				}
			}
		}
	}()

	return mgr, nil
}

func ManagerInstance() *manager {
	return mgr
}

func (mgr *manager) Create(function string, wait bool) (*FE, error) {
	y, err := ioutil.ReadFile("k8s/fe.yaml")
	if err != nil {
		return nil, err
	}

	pod := &apiv1.Pod{}
	err = yaml.Unmarshal(y, pod)
	if err != nil {
		return nil, err
	}

	// Update name with random id.
	pod.ObjectMeta.Name = fmt.Sprintf(pod.ObjectMeta.Name, uuid.New().String()[:8])

	// Update start up function
	for i, envVar := range pod.Spec.Containers[0].Env {
		switch (envVar.Name) {
		case "faas":
			envVar.Value = fmt.Sprintf(envVar.Value, function)
			pod.Spec.Containers[0].Env[i] = envVar // Must set back to take effect.
		}
	}
	pod, err = mgr.client.CoreV1().Pods(pod.ObjectMeta.Namespace).Create(pod)
	if err != nil {
		return nil, err
	}

	fe := mgr.add(function, pod)

	if wait {
		<- fe.Ready
		log.Printf("%s started", fe.Pod.Status.PodIP)
	}

	return fe, nil
}

func (mgr *manager) CreateN(function string, n int, wait bool) ([]*FE, error) {
	done := make(chan struct{})
	errchan := make(chan error, n)
	fes := make([]*FE, n)
	var readies int32

	for i := 0; i < n; i++ {
		go func(i int) {
			var err error
			fes[i], err = mgr.Create(function, false)
			errchan <- err
			if err != nil {
				return
			}

			if !wait {
				return
			}

			<-fes[i].Ready
			if atomic.AddInt32(&readies, 1) == int32(n) {
				close(done)
			}
		}(i)
	}

	for i := 0; i < n; i++ {
		err := <- errchan
		if err != nil {
			close(errchan)
			return nil, err
		}
	}
	close(errchan)

	if wait {
		<-done
	}

	return fes, nil
}

func (mgr *manager) Clean() {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	log.Printf("Start cleaning.")
	log.Printf("%v", mgr.fes)
	for _, fe := range mgr.fes {
		log.Printf("Cleaning up pod %s...", fe.Pod.ObjectMeta.Name)
		mgr.client.CoreV1().Pods(fe.Pod.ObjectMeta.Namespace).Delete(fe.Pod.ObjectMeta.Name, nil)
	}
	close(mgr.cleaned)
	log.Printf("Done.")
}

func (mgr *manager) add(function string, pod *apiv1.Pod) *FE {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	bucket, registered := mgr.funcs[function]
	if !registered {
		bucket = make(map[string]*FE)
		mgr.funcs[function] = bucket
	}

	fe := &FE{
		Pod: pod,
		Ready: make(chan struct{}),
	}
	bucket[pod.ObjectMeta.Name] = fe
	mgr.fes[pod.ObjectMeta.Name] = fe
	return fe
}

func (mgr *manager) fe(name string) *FE {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	fe, registered := mgr.fes[name]
	if registered {
		return fe
	} else {
		return nil
	}
}

// IsReadyPod checks both all containers in a pod are ready and whether
// the .metadata.DeletionTimestamp is nil.
func (mgr *manager) IsReadyPod(pod *apiv1.Pod) bool {
	// since its a utility function, just ensuring there is no nil pointer exception
	if pod == nil {
		return false
	}

	// pod is in "Terminating" status if deletionTimestamp is not nil
	// https://github.com/kubernetes/kubernetes/issues/61376
	if pod.ObjectMeta.DeletionTimestamp != nil {
		return false
	}

	for _, cStatus := range pod.Status.ContainerStatuses {
		if cStatus.Ready {
			return true
		}
	}

	return false
}
