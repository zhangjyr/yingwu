package gs

import (
	"fmt"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
//	"time"

	"github.com/zhangjyr/go-wrk/loader"
)

const (
	SchedulerPolicyOchestrator = 3
)

var (
	ERR_NO_AVAILABLE_FE = errors.New("No available fe.")
)

type SendConfig struct {
	Function        string
	Duration        int
	Concurrency     int
	MaxConcurrency  int
	Hold            bool
}

type Scheduler struct {
	sharing        int32
	queueing       int32
	mu             sync.Mutex

	policy         int

	// Ochestrator fields
	fes           []*FE
	sharePointer  int
}

func NewScheduler() *Scheduler {
	return &Scheduler{
	}
}

func NewOchestratorScheduler(fes []*FE) *Scheduler {
	return &Scheduler{
		policy: SchedulerPolicyOchestrator,
		fes: fes,
		sharePointer: len(fes) - 1,
	}
}

func (s *Scheduler) Send(fe *FE, function string, n int, concurrency int, stats chan *loader.RequesterStats, hold bool) *loader.LoadCfg {
	return s.SendWithConfig(fe, &SendConfig{ function, n, concurrency, concurrency, hold }, stats)
}

func (s *Scheduler) SendWithConfig(fe *FE, config *SendConfig, stats chan *loader.RequesterStats) *loader.LoadCfg {
	duration := config.Duration
	goroutines := config.MaxConcurrency
	allowRedirectsFlag := false
	disableCompression := false
	disableKeepAlive := false
	timeoutms := 10000
	method := "GET"
	host := ""
	headerStr := fmt.Sprintf("X-FUNCTION: %s", config.Function)
	reqBody := ""
	clientCert := ""
	clientKey := ""
	caCert := ""
	http2 := false

	loadGen := loader.NewLoadCfg(duration, goroutines, fe.Addr(), reqBody, method, host, headerStr, stats, timeoutms,
		allowRedirectsFlag, disableCompression, disableKeepAlive, clientCert, clientKey, caCert, http2)

	if config.Concurrency != config.MaxConcurrency {
		loadGen.Throttle(config.Concurrency)
	}
	if config.Hold {
		loadGen.Hold()
	}

	for i := 0; i < goroutines; i++ {
		go loadGen.RunSingleLoadSession(i, fmt.Sprintf("%s_%d", fe.Name, i))
	}

	fe.Loader = loadGen
	return loadGen
}

func (s *Scheduler) Swap(fe *FE, function string) error {
	url := fmt.Sprintf("%s_/swap/%s", fe.AdminAddr(), function)

	rsp, err := http.Post(url, "application/json", nil)
	if err == nil && rsp.StatusCode < 300 {
		// Success
		rsp.Body.Close()
		return nil
	}

	if err != nil {
		return err
	} else {
		return errors.New(fmt.Sprintf("Fail to swap %s: %d.", url, rsp.StatusCode))
	}
}

func (s *Scheduler) Share(fe *FE, function string) error {
	url := fmt.Sprintf("%s_/share/%s", fe.AdminAddr(), function)

	rsp, err := http.Post(url, "application/json", nil)
	if err == nil && rsp.StatusCode < 300 {
		// Success
		rsp.Body.Close()
		atomic.AddInt32(&s.sharing, 1)
		return nil
	}

	if err != nil {
		return err
	} else {
		return errors.New(fmt.Sprintf("Fail to share %s: %d.", url, rsp.StatusCode))
	}
}

func (s *Scheduler) Unshare(fe *FE) {
	atomic.AddInt32(&s.sharing, -1)

	go s.unshare(fe)
}

func (s *Scheduler) GetShareable() (*FE, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.policy {
	case SchedulerPolicyOchestrator:
		fe := s.fes[s.sharePointer]
		if fe != nil && fe.Loader.Threshhold() == 0 {
			s.sharePointer--
			return fe, nil
		}
		fallthrough
	default:
		return nil, ERR_NO_AVAILABLE_FE
	}
}

func (s *Scheduler) Enqueue(n int32) int32 {
	return atomic.AddInt32(&s.queueing, n)
}

func (s *Scheduler) Dequeue(n int32) int32 {
	return atomic.AddInt32(&s.queueing, -n)
}

func (s *Scheduler) Queueing() int32 {
	return atomic.LoadInt32(&s.queueing)
}

func (s *Scheduler) Sharing() int32 {
	return atomic.LoadInt32(&s.sharing)
}

func (s *Scheduler) unshare(fe *FE) {
	url := fmt.Sprintf("%s_/unshare", fe.AdminAddr())

	rsp, err := http.Post(url, "application/json", nil)
	if err == nil && rsp.StatusCode < 300 {
		// Success
		rsp.Body.Close()

		s.mu.Lock()
		defer s.mu.Unlock()

		switch s.policy {
		case SchedulerPolicyOchestrator:
			for s.sharePointer < len(s.fes) - 1 {
				if s.fes[s.sharePointer + 1].Loader.Threshhold() == 0 {
					s.sharePointer++
				}
			}
		}
	}

	if err == nil {
		errors.New(fmt.Sprintf("Fail to unshare %s: %d.", url, rsp.StatusCode))
	}

	log.Println(err.Error())
}
