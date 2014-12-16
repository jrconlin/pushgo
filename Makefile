SHELL = /bin/sh
HERE = $(shell pwd)
BIN = $(HERE)/bin
GPM = $(HERE)/gpm
DEPS = $(HERE)/.godeps
GOPATH = $(DEPS):$(HERE)
TOOLS = $(shell GOPATH=$(GOPATH) go tool)

PLATFORM=$(shell uname)

# Setup commands and env vars if there is no system go linked into bin/go
PATH := $(HERE)/bin:$(DEPS)/bin:$(PATH)

PACKAGE = github.com/mozilla-services/pushgo
TARGET = simplepush
COVER_MODE = count
COVER_PATH = $(HERE)/.coverage

VERSION = $(strip $(shell [ -e $(HERE)/GITREF ] && cat $(HERE)/GITREF 2>/dev/null))
ifeq ($(VERSION),)
	VERSION = $(strip $(shell git describe --tags --always HEAD 2>/dev/null))
endif

ifneq ($(strip $(VERSION)),)
	GOLDFLAGS := -X $(PACKAGE)/simplepush.VERSION $(VERSION) $(GOLDFLAGS)
endif


.PHONY: all build clean test $(TARGET) memcached

all: build

$(BIN):
	mkdir -p $(BIN)

$(DEPS):
	@echo "Installing dependencies"
	GOPATH=$(GOPATH) $(GPM) install
	GOPATH=$(GOPATH) go get -u github.com/mattn/goveralls

build: $(DEPS)

libmemcached-1.0.18:
	wget -qO - https://launchpad.net/libmemcached/1.0/1.0.18/+download/libmemcached-1.0.18.tar.gz | tar xvz
	cd libmemcached-1.0.18 && \
	./configure --prefix=/usr && \
	autoreconf -ivf
ifeq ($(PLATFORM),Darwin)
	cd libmemcached-1.0.18 && \
	sed -i '' $$'/ax_pthread_flags="pthreads none -Kthread -kthread lthread -pthread -pthreads -mthreads pthread --thread-safe -mt pthread-config"/c\\\nax_pthread_flags=\"pthreads none -Kthread -kthread lthread -lpthread -lpthreads -mthreads pthread --thread-safe -mt pthread-config"\n' m4/ax_pthread.m4
else
	cd libmemcached-1.0.18 && \
	sed -i '/ax_pthread_flags="pthreads none -Kthread -kthread lthread -pthread -pthreads -mthreads pthread --thread-safe -mt pthread-config"/c\ax_pthread_flags="pthreads none -Kthread -kthread lthread -lpthread -lpthreads -mthreads pthread --thread-safe -mt pthread-config"' m4/ax_pthread.m4
endif

memcached: libmemcached-1.0.18
	cd libmemcached-1.0.18 && sudo make install

$(TARGET):
	rm -f $(TARGET)
	@echo "Building simplepush"
	GOPATH=$(GOPATH) go build \
		-ldflags "$(GOLDFLAGS)" -tags libmemcached -o $(TARGET) $(PACKAGE)

$(HERE)/mockgen:
	GOPATH=$(GOPATH) go build github.com/rafrombrc/gomock/mockgen

test-mocks: $(HERE)/mockgen
	./mockgen -source=src/github.com/mozilla-services/pushgo/simplepush/config.go \
		-destination=src/github.com/mozilla-services/pushgo/simplepush/mock_config_test.go -package="simplepush"
	./mockgen -source=src/github.com/mozilla-services/pushgo/simplepush/worker.go \
		-destination=src/github.com/mozilla-services/pushgo/simplepush/mock_worker_test.go -package="simplepush"
	./mockgen -source=src/github.com/mozilla-services/pushgo/simplepush/storage.go \
		-destination=src/github.com/mozilla-services/pushgo/simplepush/mock_store_test.go -package="simplepush"
	./mockgen -source=src/github.com/mozilla-services/pushgo/simplepush/locator.go \
		-destination=src/github.com/mozilla-services/pushgo/simplepush/mock_locator_test.go -package="simplepush"
	./mockgen -source=src/github.com/mozilla-services/pushgo/simplepush/metrics.go \
		-destination=src/github.com/mozilla-services/pushgo/simplepush/mock_metrics_test.go -package="simplepush"
	# Note that to generate the log/router mock, the HasConfigStruct needs to be manually
	# copied into log.go while this is run, then the mocked config struct needs to be
	# removed from the mock_log_test.go file.
	# Issue: https://code.google.com/p/gomock/issues/detail?id=16
	#./mockgen -source=src/github.com/mozilla-services/pushgo/simplepush/log.go \
	#	-destination=src/github.com/mozilla-services/pushgo/simplepush/mock_log_test.go -package="simplepush"
	#./mockgen -source=src/github.com/mozilla-services/pushgo/simplepush/router.go \
	#	-destination=src/github.com/mozilla-services/pushgo/simplepush/mock_router_test.go -package="simplepush"

test-gomc:
	GOPATH=$(GOPATH) go test \
		-tags "memcached_server_test libmemcached" \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

test-gomemcache:
	GOPATH=$(GOPATH) go test \
		-tags memcached_server_test \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

check-cov:
ifneq (cover,$(filter cover,$(TOOLS)))
	@echo "Go tool 'Cover' not installed."
	go tool cover
	false
endif

clean-cov:
	rm -rf $(COVER_PATH)
	rm -f $(addprefix coverage,.out .html)

cov-dir: clean-cov
	mkdir -p $(COVER_PATH)

retry-cov: check-cov cov-dir
	GOPATH=$(GOPATH) go test \
		-covermode=$(COVER_MODE) -coverprofile=$(COVER_PATH)/retry.out \
		-ldflags "$(GOLDFLAGS)" $(PACKAGE)/retry

id-cov: check-cov cov-dir
	GOPATH=$(GOPATH) go test \
		-covermode=$(COVER_MODE) -coverprofile=$(COVER_PATH)/id.out \
		-ldflags "$(GOLDFLAGS)" $(PACKAGE)/id

simplepush-cov: check-cov cov-dir
	GOPATH=$(GOPATH) go test \
		-covermode=$(COVER_MODE) -coverprofile=$(COVER_PATH)/simplepush.out \
		-ldflags "$(GOLDFLAGS)" $(PACKAGE)/simplepush

# Merge coverage reports for each package. -coverprofile does not support
# multiple packages; see https://github.com/golang/go/issues/6909.
test-cov: retry-cov id-cov simplepush-cov
	echo "mode: $(COVER_MODE)" > coverage.out
	grep -h -v "^mode:" $(COVER_PATH)/*.out >> coverage.out

html-cov: test-cov
	GOPATH=$(GOPATH) go tool cover \
		-html=coverage.out -o coverage.html

travis-cov: test-cov
	GOPATH=$(GOPATH) goveralls -coverprofile=coverage.out \
		-service=travis-ci -repotoken $(COVERALLS_TOKEN)

test:
	GOPATH=$(GOPATH) go test -v \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

bench:
	GOPATH=$(GOPATH) go test -v -bench=Router -benchmem -benchtime=5s \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

vet:
	GOPATH=$(GOPATH) go vet $(addprefix $(PACKAGE)/,client id retry simplepush)

clean: clean-cov
	rm -rf bin $(DEPS)
	rm -f $(TARGET)
