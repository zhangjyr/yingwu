package policies

import (
	"fmt"
	"math"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/zhangjyr/go-wrk/loader"
)

type Stats struct {
	Aggregator     chan *loader.RequesterStats
	totalRoutings  int32
	Start          time.Time
	SigChan        chan os.Signal
}

func NewStats(concurrency int) *Stats {
	stats := &Stats{
		Aggregator: make(chan *loader.RequesterStats, concurrency * 2),
		SigChan: make(chan os.Signal, 1),
	}
	signal.Notify(stats.SigChan, os.Interrupt)
	signal.Notify(stats.SigChan, os.Kill)
	return stats
}

func (s *Stats) TotalRoutings() int32 {
	return atomic.LoadInt32(&s.totalRoutings)
}

func elapsed(start time.Time) string {
	return fmt.Sprintf("%.3fs", time.Since(start).Seconds())
}

func durationLeft(duration int, started time.Time) int {
	return int(math.Round(float64(duration) - time.Since(started).Seconds()))
}
