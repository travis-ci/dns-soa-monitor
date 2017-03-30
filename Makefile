heroku:
	cd $(GOPATH)/src/github.com/travis-ci/dns-soa-monitor && git checkout master
	go get -u github.com/travis-ci/dns-soa-monitor
	cd $(GOPATH)/src/github.com/travis-ci/dns-soa-monitor && git checkout $(SOURCE_VERSION)
	go build -o dns-soa-monitor github.com/travis-ci/dns-soa-monitor
