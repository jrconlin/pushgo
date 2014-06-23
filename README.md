Simple Push Server in Go v1.0.0
===

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

libmemcached 1.2 *note* remove older, system installed versions of
libmemcached, or you're going to have a bad time.

To build memcached from source:

1. Install following: bzr, automake, flex, bison, libtool, cloog-ppl
    * bison must be >= 2.5. You can pull the latest compy from
      http://ftp.gnu.org/gnu/bison/
2. $ wget
https://launchpad.net/libmemcached/1.0/1.0.17/+download/libmemcached-1.0.17.tar.gz
3. $ cd libmemcached-1.0.17
    * $ configure --prefix=/usr
    * $ make
    * $ sudo make install

## Installation
To install this server:

1. extract this directory into target directory
2. Run install.bash
3. You'll need the following servers running:
    * memcached
    * Hekad (optional)
4. Modify the config.ini

If you're not planning on doing development work (see previous notes
about how this is beta), you may want to build the executable with
''' go build simplepush.go '''

This will build "simplepush" as an executable.

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

To test this, or any other SimplePush server, please use [the stand
alone test suite](https://github.com/jrconlin/simplepush_test).
