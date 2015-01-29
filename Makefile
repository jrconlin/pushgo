SHELL := /bin/sh

PROTOC := protoc
CAPNPC := capnpc

BIN := $(CURDIR)/bin
DEPS := $(CURDIR)/.godeps

# Setup commands and env vars if there is no system go linked into bin/go
PATH := $(CURDIR)/bin:$(DEPS)/bin:$(PATH)
GOPATH := $(DEPS):$(CURDIR)
GPM := GOPATH=$(GOPATH) $(CURDIR)/gpm
GO := GOPATH=$(GOPATH) go

PLATFORM := $(shell uname)

# All server packages.
PACKAGE := github.com/mozilla-services/pushgo
SUBPACKAGES := $(addprefix $(PACKAGE)/,id retry simplepush)

# The executable name.
TARGET := simplepush

# Generated Protobuf and Cap'n Proto targets.
GEN_TARGETS := log_message.pb.go routable.capnp.go
GEN_PATHS := $(addprefix $(CURDIR)/src/$(PACKAGE)/simplepush/,\
	$(GEN_TARGETS))

# Interfaces for mocking.
INTERFACES := config.go worker.go storage.go locator.go metrics.go\
	balancer.go socket.go handlers.go log.go router.go proprietary_ping.go
MOCKS := $(addprefix src/$(PACKAGE)/simplepush/,\
	$(patsubst %.go,mock_%_test.go,$(INTERFACES)))

# Coverage mode; can be "set", "count", or "atomic". See
# https://blog.golang.org/cover for more info.
COVER_MODE := count

# Temporary coverage profile paths for each package, and the merged profile
# targets.
COVER_PATH := $(CURDIR)/.coverage
COVER_PACKAGES := $(addprefix $(COVER_PATH)/,\
	$(addsuffix %.out,$(SUBPACKAGES)))
COVER_TARGETS := coverage.out coverage.server.out
COVER_HTML_TARGETS := $(patsubst %.out,%.html,$(COVER_TARGETS))

.PHONY: all build gen clean-gen $(TARGET) test-mocks clean-mocks test \
	test-server test-gomc test-gomemcache check-cov travis-cov\
	html-cov html-server-cov clean-cov bench bench-server vet clean
.INTERMEDIATE: $(COVER_TARGETS)

all: build

$(BIN):
	mkdir -p $(BIN)

# Fetch and install dependencies.
$(DEPS):
	@echo "Installing dependencies"
	$(GPM) install
	$(GO) install github.com/gogo/protobuf/... \
		github.com/glycerine/go-capnproto/... \
		github.com/rafrombrc/gomock/mockgen \
		github.com/mattn/goveralls

build: $(DEPS)

# Generate Protobuf and Cap'n Proto source files.
gen: $(GEN_PATHS)

clean-gen:
	rm -f $(GEN_PATHS)

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

# Build the Simple Push server.
$(TARGET):
	rm -f $(TARGET)
	@echo "Building simplepush"
	$(GO) build -tags libmemcached -o $(TARGET) $(PACKAGE)

# Generate mock interfaces for the tests.
test-mocks: $(MOCKS)

clean-mocks:
	rm -f $(MOCKS)

mock_%_test.go: %.go
	mockgen -source=$< -destination=$@ -package="simplepush"

# Test the server with smoke tests.
test: $(MOCKS)
	$(GO) test -v -tags smoke $(SUBPACKAGES)

# Test the server only.
test-server: $(MOCKS)
	$(GO) test -v $(SUBPACKAGES)

# Test with smoke tests and the libmemcached adapter. Requires a running
# memcached cluster; can be set via PUSHGO_TEST_STORAGE_MC_HOSTS.
test-gomc: $(MOCKS)
	$(GO) test -tags "smoke memcached_server_test libmemcached" $(SUBPACKAGES)

# Test with smoke tests and the pure-Go gomemcache adapter.
test-gomemcache: $(MOCKS)
	$(GO) test -tags "smoke memcached_server_test" $(SUBPACKAGES)

# Ensure `go tool cover` is installed.
check-cov:
	@$(GO) tool -n cover >/dev/null 2>&1 || (echo \
		"Go tool 'Cover' not installed." && false)

# Generate coverage reports for the server and smoke tests.
html-cov: coverage.html

# Generate coverage reports for the server only.
html-server-cov: coverage.server.html

# Upload full coverage profile to Coveralls.
travis-cov: coverage.out check-cov
	goveralls -coverprofile=$< -service=travis-ci -repotoken $(COVERALLS_TOKEN)

clean-cov:
	rm -rf $(COVER_PATH)
	rm -f $(COVER_HTML_TARGETS)

$(COVER_PATH)/%.out: $(CURDIR)/src/%/*.go $(MOCKS)
	mkdir -p $(dir $@)
	$(GO) test -tags smoke -covermode=$(COVER_MODE) -coverprofile=$@ $*

$(COVER_PATH)/%.server.out: $(CURDIR)/src/%/*.go $(MOCKS)
	mkdir -p $(dir $@)
	$(GO) test -covermode=$(COVER_MODE) -coverprofile=$@ $*

# Merge coverage profiles for each package into an intermediate profile.
# -coverprofile does not support multiple packages; see
# https://golang.org/issue/6909.
$(COVER_TARGETS): coverage%.out: $(COVER_PACKAGES)
	echo "mode: $(COVER_MODE)" > $@
	grep -h -v "^mode:" $^ >> $@

# Generate HTML coverage reports.
$(COVER_HTML_TARGETS): %.html: %.out check-cov
	$(GO) tool cover -html=$< -o $@

# Benchmark the server and smoke tests.
bench: $(MOCKS)
	$(GO) test -v -bench . -benchmem -benchtime=5s -tags smoke $(SUBPACKAGES)

# Benchmark the server only.
bench-server: $(MOCKS)
	$(GO) test -v -bench . -benchmem -benchtime=5s $(SUBPACKAGES)

vet:
	$(GO) vet $(SUBPACKAGES)

clean: clean-cov clean-mocks clean-gen
	rm -rf bin $(DEPS)
	rm -f $(TARGET)
