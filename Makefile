HERE = $(shell pwd)
BIN = $(HERE)/bin
GODIR = $(HERE)/go
GODEP = $(BIN)/godep
DEPS = $(HERE)/Godeps/_workspace
GOPATH = $(DEPS):$(HERE):$GOPATH
GO = $(BIN)/go

GODEPCMD = GOROOT=$(HERE)/go GOPATH=$(GOPATH) $(GODEP)
GOCMD = GOROOT=$(HERE)/go GOPATH=$(GOPATH) $(GO)

SIMPLETEST = $(HERE)/simplepush_test/run_all.py
PLATFORM=$(shell uname)

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
	if ! [ -e $(GO) ]; \
	then \
		ln -s $(HERE)/go/bin/go $(GO); \
	fi;

$(BIN):
	mkdir -p $(BIN)

$(GODEP): $(BIN) $(GO)
	@echo "Installing godep"
	$(GOCMD) get github.com/tools/godep

$(DEPS): $(GODEP)
	@echo "Installing dependencies"; \
	$(GODEPCMD) restore; \

$(SIMPLETEST):
	@echo "Update git submodules"
	git submodule update --init

build: $(DEPS) $(SIMPLETEST)
	rm -f simplepush
	@echo "Building simplepush"
	$(GODEPCMD) go build github.com/mozilla-services/pushgo

clean:
	rm -rf bin $(DEPS)
	rm -f simplepush
