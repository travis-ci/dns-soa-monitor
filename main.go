package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
)

func getSerial(hostname, server string) (uint32, error) {
	m := new(dns.Msg)
	m.SetQuestion(hostname+".", dns.TypeSOA)

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

func runMonitor(hostname string, primaryServers, secondaryServers []string) {
	for {
		log.Printf("polling %s", hostname)

		maxSerial := uint32(0)
		maxSerialPrimaryServer := ""

		for _, primaryServer := range primaryServers {
			primarySerial, err := getSerial(hostname, primaryServer)
			if err != nil {
				log.Printf("error getting primary serial for %v on %v: %v\n", hostname, primaryServer, err)
				continue
			}

			if primarySerial > maxSerial {
				maxSerial = primarySerial
				maxSerialPrimaryServer = primaryServer
			}
		}

		targetServers := []string{}
		targetServers = append(targetServers, primaryServers...)
		targetServers = append(targetServers, secondaryServers...)

		for _, secondaryServer := range targetServers {
			secondarySerial, err := getSerial(hostname, secondaryServer)
			if err != nil {
				log.Printf("error: %v\n", err)
				continue
			}

			lagSeconds := int64(maxSerial) - int64(secondarySerial)

			if debug {
				log.Printf("hostname=%v primary_server=%v primary_serial=%v secondary_server=%v secondary_serial=%v lag_seconds=%v",
					hostname, maxSerialPrimaryServer, maxSerial, secondaryServer, secondarySerial, lagSeconds)
			}

			if m != nil {
				g := m.GetGauge(fmt.Sprintf("travis.dns-soa-monitor.%s.primary.%s.secondary.%s.lag_seconds", metricsify(hostname), metricsify(maxSerialPrimaryServer), metricsify(secondaryServer)))
				g <- int64(lagSeconds)
			}
		}

		time.Sleep(time.Duration(pollInterval) * time.Second)
	}
}

func main() {
	hostnames := strings.Split(os.Getenv("HOSTNAMES"), ",")
	if len(hostnames) == 0 {
		log.Fatal("please provide the HOSTNAMES env variable")
	}

	primaryServers := strings.Split(os.Getenv("PRIMARY_SERVERS"), ",")
	if len(primaryServers) == 0 {
		log.Fatal("please provide the PRIMARY_SERVERS env variable")
	}

	secondaryServers := strings.Split(os.Getenv("SECONDARY_SERVERS"), ",")
	if len(secondaryServers) == 0 {
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

	for _, hostname := range hostnames {
		go runMonitor(hostname, primaryServers, secondaryServers)
	}

	exitSignal := make(chan os.Signal)
	signal.Notify(exitSignal, syscall.SIGINT, syscall.SIGTERM)
	<-exitSignal
}
