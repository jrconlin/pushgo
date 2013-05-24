Simple Push Server in Go v0.001
===

*Please note: This server is still under radical development.*

This server was created to support the [Mozilla Simple Push protocol](https://wiki.mozilla.org/WebAPI/SimplePush). The Go language was chosen for this implementation as striking the right balance for operational support, ability to maintain lots of open websockets on AWS instances, and reasonably light weight. Some other languages and approaches showed better overall performance, many showed worse. Your milage may vary.

## Installation
To install this server:

1. extract this directory into target directory
2. Run install.bash
3. You'll need the following servers running:
    * memcached
    * Hekad (optional)
4. Modify the config.ini

If you're not planning on doing development work (see previous notes about how this is pre-beta), you may want to build the executable with
''' go build main.go '''

This will build "main" as an executable.

## Execution
The server is built to run behind a SSL capable load balancer (e.g. AWS).
For our build, we've found that AWS small instances can manage to support about 24K simultaneous websocket connections, Mediums can do about 120K, and Larges can do around 200K.

## Customization
This server currently has no facility for UDP pings. This is a proprietary function (which, unsurprisingly, works remarkably poorly with non-local networks). There is currently no "dashboard" for element management.

## Use
That's neat and all, but what does this do?

Well, it's easy for a device like a phone to call into a server. After all, most servers don't fall off the network or get their IP address radically changed. This service tries to solve for that by providing a websocket on one side that that reacts when a remote server pokes it.

Honestly, go read the specification. It does a better job of explaining things.

