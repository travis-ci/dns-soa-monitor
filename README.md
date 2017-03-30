# travis-ci/dns-soa-monitor

A mini monitor to keep track of dns zone transfer (replication) lag by comparing the serial of the SOA records.

Also optionally reports replication lag to librato.

It's essentially an automated way of running:

```
➜  ~ for ns in ns3.dnsimple.com. ns7.dnsmadeeasy.com.; do dig @$ns travis-ci.org soa +short; done
axfr.dnsimple.com. admin.dnsimple.com. 1430416865 86400 7200 604800 300
axfr.dnsimple.com. admin.dnsimple.com. 1430416861 86400 7200 604800 300
```

## Settings

* `HOSTNAMES` - a comma-separated list of host names to monitor, e.g. `travis-ci.org,travis-ci.com`.
* `PRIMARY_SERVER` - the hostname of the primary server. e.g. `ns1.dnsimple.com`.
* `SECONDARY_SERVERS` - a comma-separated list of secondary servers to monitor. e.g. `ns5.dnsmadeeasy.com,ns6.dnsmadeeasy.com,ns7.dnsmadeeasy.com`.
* `POLL_INTERVAL` - the number of seconds to wait in between polls. Defaults to `60` seconds.
* `LIBRATO_USER` - (optional) the librato user, usually looks like an email address.
* `LIBRATO_TOKEN` - (optional) the librato token.
* `LIBRATO_SOURCE` - (optional) the librato source. If none is provided, it will attempt to use the `DYNO` env var. If that is empty, it will use the hostname of the machine running the monitor.
* `DEBUG` - (optional) Set to `true` to get more verbose debug logging. Defaults to `false`.

## Install

    $ go get -u github.com/FiloSottile/gvt
    $ gvt restore

## Running

    $ go run main.go