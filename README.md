Simple Push Server in Go v1.5.0
===

![PushGo Logo](https://cdn.rawgit.com/mozilla-services/pushgo/dev/logo.jpg)

This server was created to support the [Mozilla Simple Push
protocol](https://wiki.mozilla.org/WebAPI/SimplePush). The Go language
was chosen for this implementation as striking the right balance for
operational support, ability to maintain lots of open websockets on
AWS instances, and reasonably light weight. Some other languages and
approaches showed better overall performance, many showed worse. Your
milage may vary.

Please note: PushGo is not a reference implementation of the SimplePush
protocol. It was created in order to support large numbers (1,000,000+
simultaneously connected users) in a cost effective manner. As such, some
features of the protocol are not present, (e.g. message retry, client state
recording, closed channel responses for third party servers, etc.)

## System requirements.

If you require offline storage (e.g. for mobile device usage), we
currently recommend memcache storage.

You will need to have Go 1.3 or higher installed on your system, and the
GOROOT and PATH should be set appropriately for 'go' to be found.

## Compiling
Check out a working copy of the source code with Git:

    git clone https://github.com/mozilla-services/pushgo.git
    cd pushgo

The server includes three adapters for offline storage: `memcache_gomc` and
`memcache_memcachego`, which persist to memcached, and `dynamodb`, which
persists to DynamoDB.

### memcached persistence

The `memcache_gomc` adapter (`emcee_store.go`) binds to libmemcached via
[gomc](http://godoc.org/github.com/varstr/gomc). This library provides a great
deal of control over how the server communicates and uses memcache, however it
does require compiling a local version of libmemcache, which can add difficulty.

The `memcache_memcachego` adapter (`gomemc_store.go`) uses a golang based
memcache client. While fully functional, we've not tested this under full load.

### Custom compilers

If you would like to build with a specific version of `go`, `protoc`,
or `capnpc` on your system:

1. Create a `bin` directory in the target directory
2. Symlink the desired binary into the `bin` directory you made

### Building

To build the server with all storage adapters, run:

    make
    make simplepush

To build the server **without the libmemcached adapter** (`memcache_gomc`),
run:

    make
    make simplepush-no-gomc

This will build a `simplepush` or `simplepush-no-gomc` executable, which you
can run like so:

    cp config.sample.toml config.toml
    # Edit config.toml appropriately
    ./simplepush -config=config.toml
    # Or ./simplepush-no-gomc -config=config.toml

## Execution
 The server is built to run behind a SSL capable load balancer (e.g.
AWS). For our build, we've found that AWS small instances can manage
to support about 24K simultaneous websocket connections, Mediums can
do about 120K, and Larges can do around 200K.

## Customization
This server currently has no facility for UDP pings. This is a
proprietary function (which, unsurprisingly, works remarkably poorly
with non-local networks). There is currently no "dashboard" for
element management.

## Use
That's neat and all, but what does this do?

Well, it's easy for a device like a phone to call into a server.
After all, most servers don't fall off the network or get their IP
address radically changed. This service tries to solve for that by
providing a websocket on one side that that reacts when a remote
server pokes it.

Honestly, go read the specification. It does a better job of
explaining things.

## Testing

To run the unit tests:

    make test

To run the integration tests with the libmemcached adapter (`memcache_gomc`;
requires a running memcached instance. memcached listens on port 11211
by default):

    PUSHGO_TEST_STORAGE_MEMCACHE_SERVER={host:port} make test-gomc

To run the tests with the `memcache_memcachego` adapter:

    PUSHGO_TEST_STORAGE_MEMCACHE_SERVER={host:port} make test-gomemcache

You can also use the Python-based
[stand-alone test suite](https://github.com/mozilla-services/simplepush_test)
to test this or any other SimplePush server:

    git clone https://github.com/mozilla-services/simplepush_test.git
    cd simplepush_test
    make
    PUSH_SERVER=ws://{host:port}/ make test
    PUSH_SERVER=ws://{host:port}/ ./bin/nosetests test_simple test_loop

## Docker

Pushgo is available in a docker container for easy deployment.

Install the container:

* docker pull bbangert/pushgo:dev

It's recommended that you create a config.toml as described above, and
then volume mount it into the container. If you had your config as
``config/pushgo.toml`` then you could run:

* docker run --rm -v `pwd`/config:/opt/config bbangert/pushgo:dev -config="/opt/config/pushgo.toml"
