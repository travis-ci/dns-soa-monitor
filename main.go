package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"github.com/pkg/errors"
	librato "github.com/rcrowley/go-librato"
)

var (
	pollInterval = 60
	m            librato.Metrics
	debug        = false
	errorCount   = uint64(0)
)

func runErrorCountReporter() {
	if m == nil && !debug {
		return
	}

	for {
		count := atomic.LoadUint64(&errorCount)

		if m != nil {
			c := m.GetCounter(fmt.Sprintf("travis.dns-soa-monitor.errors"))
			c <- int64(count)
		}

		if debug {
			log.Printf("error_count=%v", errorCount)
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

func metricsify(s string) string {
	return strings.Replace(s, ".", "_", -1)
}

func runDomainMonitor(domainName string, primaryServers, secondaryServers []string) {
	for {
		log.Printf("polling %s", domainName)

		maxSerial := uint32(0)
		maxSerialPrimaryServer := ""

		for _, primaryServer := range primaryServers {
			primarySerial, err := getSerial(domainName, primaryServer)
			if err != nil {
				log.Printf("error getting primary serial for %v on %v: %v\n", domainName, primaryServer, err)
				atomic.AddUint64(&errorCount, 1)
				continue
			}

			if primarySerial > maxSerial {
				maxSerial = primarySerial
				maxSerialPrimaryServer = primaryServer
			}
		}

		if maxSerialPrimaryServer == "" {
			log.Printf("error: no primary server responded for %v\n", domainName)
			atomic.AddUint64(&errorCount, 1)
			continue
		}

		targetServers := []string{}
		targetServers = append(targetServers, primaryServers...)
		targetServers = append(targetServers, secondaryServers...)

		for _, secondaryServer := range targetServers {
			secondarySerial, err := getSerial(domainName, secondaryServer)
			if err != nil {
				log.Printf("error: %v\n", err)
				atomic.AddUint64(&errorCount, 1)
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

	debug = os.Getenv("DEBUG") == "true"

	go runErrorCountReporter()

	for _, domainName := range domainNames {
		go runDomainMonitor(domainName, primaryServers, secondaryServers)
	}

	exitSignal := make(chan os.Signal)
	signal.Notify(exitSignal, syscall.SIGINT, syscall.SIGTERM)
	<-exitSignal
}
