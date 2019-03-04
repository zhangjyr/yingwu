package gs

import (
	"fmt"
	"errors"
	"net/http"
	"strings"
//	"time"

	"github.com/tsliwowicz/go-wrk/loader"
)

type Scheduler struct {

}

func NewScheduler() *Scheduler {
	return &Scheduler{

	}
}

func (s *Scheduler) Send(fe *FE, function string, n int, concurrency int, stats chan *loader.RequesterStats, hold bool) *loader.LoadCfg {
	duration := n
	goroutines := concurrency
	allowRedirectsFlag := false
	disableCompression := false
	disableKeepAlive := false
	timeoutms := 1000
	method := "GET"
	host := ""
	headerStr := fmt.Sprintf("X-FUNCTION: %s", function)
	reqBody := ""
	clientCert := ""
	clientKey := ""
	caCert := ""
	http2 := false

	header := make(map[string]string)
	if headerStr != "" {
		headerPairs := strings.Split(headerStr, ";")
		for _, hdr := range headerPairs {
			hp := strings.Split(hdr, ":")
			header[hp[0]] = hp[1]
		}
	}

	loadGen := loader.NewLoadCfg(duration, goroutines, fe.Addr(), reqBody, method, host, header, stats, timeoutms,
		allowRedirectsFlag, disableCompression, disableKeepAlive, clientCert, clientKey, caCert, http2)
	if hold {
		loadGen.Hold()
	}

	for i := 0; i < goroutines; i++ {
		go loadGen.RunSingleLoadSession()
	}

	return loadGen
}

func (s *Scheduler) Share(fe *FE, function string) error {
	url := fmt.Sprintf("%s_/share/%s", fe.AdminAddr(), function)

	rsp, err := http.Post(url, "application/json", nil)
	if err == nil && rsp.StatusCode < 300 {
		// Success
		rsp.Body.Close()
		return nil
	}

	if err != nil {
		return err
	} else {
		return errors.New(fmt.Sprintf("Fail to share %s: %d.", url, rsp.StatusCode))
	}
}
