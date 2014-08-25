SHELL = /bin/sh
HERE = $(shell pwd)
BIN = $(HERE)/bin
GODEP = $(BIN)/godep
DEPS = $(HERE)/Godeps/_workspace
GOPATH := $(DEPS):$(HERE):$(GOPATH)
GOBIN = $(BIN)

SYSTEMGO = $(BIN)/go

PLATFORM=$(shell uname)

# Setup commands and env vars if there is no system go linked into bin/go
PATH := $(HERE)/bin:$(PATH)

.PHONY: all build clean test simplepush memcached

all: build

$(BIN):
	mkdir -p $(BIN)

$(GODEP): $(BIN)
	@echo "Installing godep"
	go get github.com/tools/godep
	if [ -e $(DEPS)/bin/godep ] ; then \
		mv $(DEPS)/bin/godep $(BIN)/godep; \
	fi;

$(DEPS): $(GODEP)
	@echo "Installing dependencies"
	$(GODEP) restore

build: $(DEPS)

libmemcached-1.0.18:
	wget https://launchpad.net/libmemcached/1.0/1.0.18/+download/libmemcached-1.0.18.tar.gz
	tar xzvf libmemcached-1.0.18.tar.gz
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

simplepush:
	rm -f simplepush
	@echo "Building simplepush"
	$(GODEP) go build -o simplepush github.com/mozilla-services/pushgo

test:
	$(GODEP) go test github.com/mozilla-services/pushgo/client github.com/mozilla-services/pushgo/id

clean:
	rm -rf bin $(DEPS)
	rm -f simplepush
