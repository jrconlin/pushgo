SHELL = /bin/sh
HERE = $(shell pwd)
BIN = $(HERE)/bin
GODEP = $(BIN)/godep
DEPS = $(HERE)/Godeps/_workspace
GOPATH := $(DEPS):$(HERE)
GOBIN = $(BIN)

PLATFORM=$(shell uname)

# Setup commands and env vars if there is no system go linked into bin/go
PATH := $(HERE)/bin:$(DEPS)/bin:$(PATH)

PACKAGE = github.com/mozilla-services/pushgo

PROTO = $(HERE)/src/$(PACKAGE)/message

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

protobuf-2.6.0:
	wget -qO - https://protobuf.googlecode.com/svn/rc/protobuf-2.6.0.tar.gz | tar xvz
	cd protobuf-2.6.0 && \
	./configure

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

protobuf: protobuf-2.6.0
	cd protobuf-2.6.0 && make install

proto:
	protoc --gogo_out=$(PROTO) \
		-I=$(PROTO):$(DEPS)/src/code.google.com/p/gogoprotobuf/gogoproto:$(DEPS)/src/code.google.com/p/gogoprotobuf/protobuf \
		$(wildcard $(PROTO)/*.proto)

simplepush:
	rm -f simplepush
	@echo "Building simplepush"
	$(GODEP) go build -o simplepush github.com/mozilla-services/pushgo

test:
	$(GODEP) go test $(addprefix $(PACKAGE)/,id simplepush)

vet:
	$(GODEP) go vet $(addprefix $(PACKAGE)/,client id simplepush)

clean:
	rm -rf bin $(DEPS)
	rm -f simplepush
