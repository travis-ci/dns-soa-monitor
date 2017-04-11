package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	raven "github.com/getsentry/raven-go"
	"github.com/miekg/dns"
	"github.com/paulbellamy/ratecounter"
	"github.com/pkg/errors"
	librato "github.com/rcrowley/go-librato"
)

var (
	pollInterval     = 60
	m                librato.Metrics
	debug            = false
	errorRateCounter = ratecounter.NewRateCounter(60 * time.Second)
)

func runErrorCountReporter() {
	if m == nil && !debug {
		return
	}

	for {
		errorRate := errorRateCounter.Rate() / 60

		if m != nil {
			c := m.GetCounter(fmt.Sprintf("travis.dns-soa-monitor.error_rate"))
			c <- int64(errorRate)
		}

		if debug {
			log.Printf("error_rate=%v", errorRate)
		}

		time.Sleep(time.Duration(pollInterval) * time.Second)
	}
}

func getSerial(domainName, server string) (uint32, error) {
	m := new(dns.Msg)
	m.SetQuestion(domainName+".", dns.TypeSOA)

	r, err := dns.Exchange(m, server+":53")
	if err != nil {
		return 0, errors.Wrapf(err, "failed to exchange")
	}
	if r == nil || r.Rcode != dns.RcodeSuccess {
		return 0, errors.Wrapf(err, "failed to get an valid answer")
	}

	if len(r.Answer) == 0 {
		return 0, errors.New("no records returned for soa query")
	}

	if len(r.Answer) > 1 {
		return 0, errors.New("too many records returned for soa query")
	}

	if t, ok := r.Answer[0].(*dns.SOA); ok {
		return t.Serial, nil
	}

	return 0, errors.New("no soa record returned")
}

func getSerials(domainName string, targetServers []string, errs chan<- error) map[string]uint32 {
	serials := make(map[string]uint32)
	var mutex sync.Mutex

	var wg sync.WaitGroup
	for _, server := range targetServers {
		wg.Add(1)
		go func(server string) {
			defer wg.Done()
			serial, err := getSerial(domainName, server)
			if err != nil {
				errs <- err
				return
			}
			mutex.Lock()
			serials[server] = serial
			mutex.Unlock()
		}(server)
	}
	wg.Wait()

	close(errs)

	return serials
}

func metricsify(s string) string {
	return strings.Replace(s, ".", "_", -1)
}

func processError(err error) {
	log.Printf("error: %v", err)
	errorRateCounter.Incr(1)
	raven.CaptureErrorAndWait(err, nil)
}

func runDomainMonitor(domainName string, primaryServers, secondaryServers []string) {
	// remember max serial between polls
	maxSerial := uint32(0)
	maxSerialPrimaryServer := ""

	for {
		log.Printf("polling %s", domainName)

		targetServers := []string{}
		targetServers = append(targetServers, primaryServers...)
		targetServers = append(targetServers, secondaryServers...)

		errs := make(chan error)
		go func() {
			for err := range errs {
				processError(err)
			}
		}()

		serials := getSerials(domainName, targetServers, errs)

		for _, primaryServer := range primaryServers {
			primarySerial, ok := serials[primaryServer]
			if !ok {
				continue
			}

			if primarySerial > maxSerial {
				maxSerial = primarySerial
				maxSerialPrimaryServer = primaryServer
			}
		}

		if maxSerialPrimaryServer == "" {
			err := errors.Errorf("no primary server responded for %v", domainName)
			processError(err)
			continue
		}

		maxLagSeconds := int64(0)

		for _, secondaryServer := range targetServers {
			secondarySerial, ok := serials[secondaryServer]
			if !ok {
				continue
			}

			lagSeconds := int64(maxSerial) - int64(secondarySerial)

			if debug {
				log.Printf("domain_name=%v primary_server=%v primary_serial=%v secondary_server=%v secondary_serial=%v lag_seconds=%v",
					domainName, maxSerialPrimaryServer, maxSerial, secondaryServer, secondarySerial, lagSeconds)
			}

			if m != nil {
				g := m.GetGauge(fmt.Sprintf("travis.dns-soa-monitor.%s.primary.%s.secondary.%s.lag_seconds", metricsify(domainName), metricsify(maxSerialPrimaryServer), metricsify(secondaryServer)))
				g <- int64(lagSeconds)
			}

			if lagSeconds > maxLagSeconds {
				maxLagSeconds = lagSeconds
			}
		}

		if debug {
			log.Printf("domain_name=%v max_lag_seconds=%v", domainName, maxLagSeconds)
		}

		if m != nil {
			g := m.GetGauge(fmt.Sprintf("travis.dns-soa-monitor.%s.max_lag_seconds", metricsify(domainName)))
			g <- int64(maxLagSeconds)
		}

		time.Sleep(time.Duration(pollInterval) * time.Second)
	}
}

func main() {
	domainNames := strings.Split(os.Getenv("DOMAIN_NAMES"), ",")
	if os.Getenv("DOMAIN_NAMES") == "" {
		log.Fatal("please provide the DOMAIN_NAMES env variable")
	}

	primaryServers := strings.Split(os.Getenv("PRIMARY_SERVERS"), ",")
	if os.Getenv("PRIMARY_SERVERS") == "" {
		log.Fatal("please provide the PRIMARY_SERVERS env variable")
	}

	secondaryServers := strings.Split(os.Getenv("SECONDARY_SERVERS"), ",")
	if os.Getenv("SECONDARY_SERVERS") == "" {
		log.Fatal("please provide the SECONDARY_SERVERS env variable")
	}

	var err error
	if os.Getenv("POLL_INTERVAL") != "" {
		pollInterval, err = strconv.Atoi(os.Getenv("POLL_INTERVAL"))
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("running with POLL_INTERVAL of %v", pollInterval)
	} else {
		log.Printf("defaulting POLL_INTERVAL to %v", pollInterval)
	}

	if os.Getenv("LIBRATO_USER") != "" && os.Getenv("LIBRATO_TOKEN") != "" {
		source := os.Getenv("LIBRATO_SOURCE")
		if source == "" {
			source = os.Getenv("DYNO")
		}
		if source == "" {
			source, err = os.Hostname()
			if err != nil {
				log.Fatal(err)
			}
		}

		m = librato.NewSimpleMetrics(
			os.Getenv("LIBRATO_USER"),
			os.Getenv("LIBRATO_TOKEN"),
			source,
		)
		defer m.Wait()
		defer m.Close()
	} else {
		log.Print("no librato config provided, to enable librato, please provide LIBRATO_USER and LIBRATO_TOKEN")
	}

	if os.Getenv("SENRTY_DSN") != "" {
		err := raven.SetDSN(os.Getenv("SENRTY_DSN"))
		if err != nil {
			log.Fatal(err)
		}

		// TODO: raven.SetRelease(VersionString)
		if os.Getenv("SENRTY_ENVIRONMENT") != "" {
			raven.SetEnvironment(os.Getenv("SENRTY_ENVIRONMENT"))
		}
	}

	debug = os.Getenv("DEBUG") == "true"

	go raven.CapturePanicAndWait(runErrorCountReporter, nil)

	for _, domainName := range domainNames {
		go func(domainName string, primaryServers, secondaryServers []string) {
			raven.CapturePanicAndWait(func() {
				runDomainMonitor(domainName, primaryServers, secondaryServers)
			}, nil)
		}(domainName, primaryServers, secondaryServers)
	}

	exitSignal := make(chan os.Signal)
	signal.Notify(exitSignal, syscall.SIGINT, syscall.SIGTERM)
	<-exitSignal
}
