HERE = $(shell pwd)
GOBIN = $(HERE)/bin
GODEP = $(GOBIN)/godep
DEPS = $(HERE)/Godeps/_workspace
GOPATH=$(DEPS):$(HERE):$GOPATH
GO = $(GOBIN)/go
GODIR = $(HERE)/go
GOCMD = GOROOT=$(GOROOT) $(GO)
SIMPLETEST = $(HERE)/simplepush_test/run_all.py
PLATFORM=$(shell uname)
GOROOT ?= $(HERE)/go

.PHONY: all build clean test

all: build

$(GODIR):
ifeq ($(PLATFORM),Darwin)
	curl -O https://storage.googleapis.com/golang/go1.3.1.darwin-amd64-osx10.8.tar.gz
else
	curl -O https://storage.googleapis.com/golang/go1.3.1.linux-amd64.tar.gz
endif
	tar xzvf go1.3.1.*.tar.gz
	rm go1.3.1*.tar.gz

$(GO): $(GODIR)
	ln -s $(HERE)/go/bin/go $(GO)

$(GOBIN):
	mkdir -p $(GOBIN)

$(GODEP): $(GOBIN) $(GO)
	@echo "Installing godep"
	$(GOCMD) get github.com/tools/godep

$(DEPS): $(GODEP)
	@echo "Installing dependencies"
	$(GODEP) restore

$(SIMPLETEST):
	@echo "Update git submodules"
	git submodule update --init

build: $(DEPS) $(SIMPLETEST)
	rm -f simplepush
	@echo "Building simplepush"
	$(GOCMD) build -o simplepush github.com/mozilla-services/pushgo

clean:
	rm -rf bin $(DEPS)
