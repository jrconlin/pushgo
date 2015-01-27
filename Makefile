SHELL = /bin/sh
GO = go
PROTOC = protoc
CAPNPC = capnpc

HERE = $(shell pwd)
BIN = $(HERE)/bin
GPM = $(HERE)/gpm
DEPS = $(HERE)/.godeps
GOPATH = $(DEPS):$(HERE)
TOOLS = $(shell GOPATH=$(GOPATH) $(GO) tool)

PLATFORM=$(shell uname)

# Setup commands and env vars if there is no system go linked into bin/go
PATH := $(HERE)/bin:$(DEPS)/bin:$(PATH)

PACKAGE = github.com/mozilla-services/pushgo
PREFIX = src/$(PACKAGE)/simplepush
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
	GOPATH=$(GOPATH) $(GO) install github.com/gogo/protobuf/... \
		github.com/rafrombrc/gomock/mockgen \
		github.com/mattn/goveralls

build: $(DEPS)

gen: $(addprefix $(HERE)/$(PREFIX)/,log_message.pb.go routable.capnp.go)

%.pb.go: %.proto
	$(PROTOC) -I$(DEPS)/src/github.com/gogo/protobuf/gogoproto \
		-I$(DEPS)/src/github.com/gogo/protobuf/protobuf \
		-I$(dir $<) --gogo_out=$(dir $<) $<

%.capnp.go: %.capnp
	$(CAPNPC) -I$(dir $<) -ogo:$(dir $<) $<

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
	GOPATH=$(GOPATH) $(GO) build \
		-ldflags "$(GOLDFLAGS)" -tags libmemcached -o $(TARGET) $(PACKAGE)

MOCK_INTERFACES = config.go worker.go storage.go locator.go metrics.go\
	balancer.go server.go socket.go handlers.go log.go router.go\
	proprietary_ping.go

mock_%_test.go: %.go $(DEPS)
	mockgen -source=$< -destination=$@ -package="simplepush"

test-mocks: $(addprefix $(PREFIX)/,$(patsubst %.go,mock_%_test.go,\
	$(MOCK_INTERFACES)))

clean-mocks:
	rm -f $(addprefix $(PREFIX)/,$(patsubst %.go,mock_%_test.go,\
		$(MOCK_INTERFACES)))

test-gomc:
	GOPATH=$(GOPATH) $(GO) test \
		-tags "smoke memcached_server_test libmemcached" \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

test-gomemcache:
	GOPATH=$(GOPATH) $(GO) test \
		-tags "smoke memcached_server_test" \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

check-cov:
ifneq (cover,$(filter cover,$(TOOLS)))
	@echo "Go tool 'Cover' not installed."
	$(GO) tool cover
	false
endif

clean-cov:
	rm -rf $(COVER_PATH)
	rm -f $(addprefix coverage,.out .html)

cov-dir: clean-cov
	mkdir -p $(COVER_PATH)

retry-cov: check-cov cov-dir
	GOPATH=$(GOPATH) $(GO) test \
		-covermode=$(COVER_MODE) -coverprofile=$(COVER_PATH)/retry.out \
		-ldflags "$(GOLDFLAGS)" $(PACKAGE)/retry

id-cov: check-cov cov-dir
	GOPATH=$(GOPATH) $(GO) test \
		-covermode=$(COVER_MODE) -coverprofile=$(COVER_PATH)/id.out \
		-ldflags "$(GOLDFLAGS)" $(PACKAGE)/id

simplepush-cov: check-cov cov-dir
	GOPATH=$(GOPATH) $(GO) test \
		-covermode=$(COVER_MODE) -coverprofile=$(COVER_PATH)/simplepush.out \
		-tags smoke \
		-ldflags "$(GOLDFLAGS)" $(PACKAGE)/simplepush

# Merge coverage reports for each package. -coverprofile does not support
# multiple packages; see https://github.com/golang/go/issues/6909.
test-cov: retry-cov id-cov simplepush-cov
	echo "mode: $(COVER_MODE)" > coverage.out
	grep -h -v "^mode:" $(COVER_PATH)/*.out >> coverage.out

html-cov: test-cov
	GOPATH=$(GOPATH) $(GO) tool cover \
		-html=coverage.out -o coverage.html

travis-cov: test-cov
	GOPATH=$(GOPATH) goveralls -coverprofile=coverage.out \
		-service=travis-ci -repotoken $(COVERALLS_TOKEN)

test: test-mocks
	GOPATH=$(GOPATH) $(GO) test -v \
		-tags smoke \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

bench:
#	GOPATH=$(GOPATH) $(GO) test -v -bench=Router -benchmem -benchtime=5s
	GOPATH=$(GOPATH) $(GO) test -v -bench . -benchmem -benchtime=5s \
		-tags smoke \
		-ldflags "$(GOLDFLAGS)" $(addprefix $(PACKAGE)/,id retry simplepush)

vet:
	GOPATH=$(GOPATH) $(GO) vet $(addprefix $(PACKAGE)/,client id retry simplepush)

clean: clean-cov clean-mocks
	rm -rf bin $(DEPS)
	rm -f $(TARGET)
