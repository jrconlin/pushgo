HERE = $(shell pwd)
BIN = $(HERE)/bin
GODIR = $(HERE)/go
GODEP = $(BIN)/godep
DEPS = $(HERE)/Godeps/_workspace
GOPATH = $(DEPS):$(HERE):$GOPATH

SYSTEMGO = $(BIN)/go

SIMPLETEST = $(HERE)/simplepush_test/run_all.py
PLATFORM=$(shell uname)

# Setup commands and env vars if there is no system go linked into bin/go
ifeq ("$(wildcard $(SYSTEMGO))", "")
GO = $(HERE)/go/bin/go
GODEPCMD = GOROOT=$(HERE)/go GOPATH=$(GOPATH) $(GODEP)
GOCMD = GOROOT=$(HERE)/go GOPATH=$(GOPATH) $(GO)
USESYSTEM = 0
else
GO = $(SYSTEMGO)
GODEPCMD = $(GODEP)
GOCMD = $(GO)
USESYSTEM = 1
endif

.PHONY: all build clean test simplepush

all: build

$(GODIR):
ifeq ($(PLATFORM),Darwin)
	curl -O https://storage.googleapis.com/golang/go1.3.1.darwin-amd64-osx10.8.tar.gz
else
	curl -O https://storage.googleapis.com/golang/go1.3.1.linux-amd64.tar.gz
endif
	tar xzvf go1.3.1.*.tar.gz
	rm go1.3.1*.tar.gz

# Download go if we're not using the system go that someone linked as bin/go
ifeq ($(USESYSTEM), 0)
$(GO): $(GODIR)
else
$(GO):
endif

$(BIN):
	mkdir -p $(BIN)

$(GODEP): $(BIN) $(GO)
	@echo "Installing godep"
	$(GOCMD) get github.com/tools/godep

$(DEPS): $(GODEP)
	@echo "Installing dependencies"
	$(GODEPCMD) restore

$(SIMPLETEST):
	@echo "Update git submodules"
	git submodule update --init

build: $(DEPS) $(SIMPLETEST)

simplepush:
	rm -f simplepush
	@echo "Building simplepush"
	$(GODEPCMD) go build -o simplepush github.com/mozilla-services/pushgo

clean:
	rm -rf bin $(DEPS)
	rm -f simplepush
