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

If you require offline storage (e.g. for mobile device usage), we
currently recommend memcache storage.

## Installation
To install this server:

1. extract this directory into target directory
2. Run install.bash
3. You'll need the following servers running:
4. Modify the config.ini

If you're not planning on doing development work (see previous notes
about how this is beta), you may want to build and run the executable
with the ''' run ''' command

This will build "pushgo" as an executable.

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

## Docker

Pushgo is available in a docker container for easy deployment.

Install the container:

* docker pull bbangert/pushgo:dev

It's recommended that you create a config.ini as described above, and
then volume mount it into the container. If you had your config as
``config/pushgo.ini`` then you could run:

* docker run --rm -v `pwd`/config:/opt/config bbangert/pushgo:dev -config="/opt/config/push.ini"
