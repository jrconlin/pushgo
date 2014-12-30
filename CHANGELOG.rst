=========
Changelog
=========

1.5
===

Features
--------

- Added support for "data". Data is not stored for offline devices
  (mostly for privacy reasons). Connected devices will get up to system
  configurable bytes (defaults to 4096) of data included in the update
  message. PR #135
- New config option for how many idle connections per host (for the router)
  are allowed:
    idle_conns
- New config option for GCM proprietary ping for idle connections:
    idle_conns
- Always route option, allows the router to always attempt to route a message
  before checking local clients. This can be useful to increase message
  delivery reliability at the expense of some additional routing. PR #116.
    always_route
- Weighted Load balancing via redirect using etcd. pushgo can now store client
  counts in etcd and utilize counts to do a weighted redirect during a client's
  hello. The client will get a 307 if a peer is available, and a 429 if the
  entire cluster is full. This add's a new [balancer] section to the config
  file per the config.sample.toml. PR #144.
- Graceful shutdown has been added. When a running server encounters an error
  or receives an interrupt signal, it stops the endpoint listener, deregisters
  from the balancer, closes all client connections, deregisters from the
  discovery service, and stops the routing listener. By default, servers will
  wait for 2 refresh periods after removing their keys from etcd to ensure the
  changes propagate to all peers. PR #178, Issue #139.
- New config option for specifying the shutdown delay for [discovery] and
  [balancer]. PR #178.
    close_delay

Bug Fixes
---------

- Don't log 'connection timed out' or 'TLS handshake error' websocket
  disconnects as ERROR. Instead they will be skipped as they occur in a normal
  situation and don't reflect an error in the server. Issue #68.
- Fix for persistent connection handling in the router. Previously the router
  was failing to properly re-use TCP connections. PR #153.
- Removed duplicate UAID check. Fast reconnects previously could've been
  denied as dupe UAID's on a single host were not allowed. We now boot prior
  clients of the same UAID on the same host, and allow the newest connection.
  PR #152, Issue #141.
- Fix for sending wrong websocket ping format. Pings should be sent as text
  frames, due to a bug they were sent as binary frames. PR #147.
- The update endpoint uses 'application/x-www-form-urlencoded' for PUT requests
  without a 'Content-Type' header. PR #160.

Metrics
-------

- Router and Endpoint connections now store counter metrics so total endpoint
  socket connections and router connection re-use is visible.
    'endpoint.socket.connect', 'endpoint.socket.disconnect'
    'router.socket.connect', 'router.socket.disconnect'
- Additional router TCP dial counters:
    'router.dial.error', 'router.dial.success'
- Timer for the websocket endpoints have changed:
    'socket.lifespan' -> 'client.socket.lifespan'
- Counter for websocket connection has changed:
    'socket.disconnect' -> 'client.socket.disconnect'
    'socket.connect'    -> 'client.socket.connect'
- GCM counters:
    'ping.gcm.retry', 'ping.gcm.error', 'ping.gcm.success'
- Etcd Balancer:
    'balancer.fetch.retry', 'balancer.fetch.error'
    'balancer.fetch.success', 'balancer.publish.retry'
    'balancer.publish.error', 'balancer.publish.success'
    'balancer.etcd.error', 'balancer.etcd.retry'

Incompatibilities
-----------------

- The 'pool_size' option for [router] has been removed. PR #167.
- The [discovery] 'max_retries', 'retry_delay', 'max_jitter', and 'max_delay'
  options have moved to the [discovery.retry] section. 'max_retries' has
  been renamed to 'retries', and 'retry_delay' is now 'delay'. PR #145.
- Origins is no longer a [default] value in the config.toml, it is now under
  the [websocket] section. Config files and env vars will need to be updated
  for this change. PR #178, Issues #139, #142.
- The [default.websocket] section has been renamed to [websocket.listener].
  PR #178.
- The [default.endpoint] section has been renamed to [endpoint.listener].
  PR #178.
- The [discovery] default values for 'defaultTTL' and 'refresh_interval' have
  changed to "1m" and "10s," respectively, to reflect production usage.
  PR #178.

GCM
---

- Sends data.
- Uses new retry system for more reliable delivery.

Internal
--------

- Router has been re-factored to an interface, and the default router is now
  known as the BroadcastRouter. PR #154, Issue #127.
- Router now exposes a health check used by '/realstatus'. PR #178, Issue #156.
- Mocks for the router and most other interfaces in pushgo have been generated
  by gomock. Multiple PR's.
- A ServeCloser type has been added for wrapping an HTTP server with shutdown
  capability. PR #178.
- The websocket, endpoint, and health handlers have been refactored into
  separate types to support graceful shutdown. PR #178.
- The PRNG now uses a cryptographically-strong seed. PR #178.
- Muxes for the websocket, endpoint, router handlers are now exposed for easier
  testing and mocking.
- The router now uses a goroutine per notification instead of a goroutine pool.
  This ensures slow requests don't delay other requests. PR #167.
- The Heka client dependency has been removed. PR #161, Issue #125.

1.4.2
=====

This release is a server maintenance release which should not impact
client or API usage.

Bug Fixes
---------

- Run memcached tests on Travis build system
- Fix bug for nil pointer with ping messages
- Improve error reporting around message routing
- Improve message parsing
- Add missing config options to sample config file
- Add sub-product name to logging data (loop, simplepush, etc.)

1.4.1
=====

This release is a server maintenance release which should not impact
client or API usage. Client should see some improvements in message
handling and response at very high loads.

Bug Fixes
---------

- Improvements to reduce cost of metric reporting
- Improvements to intramachine message routing
- Improvements to internal UAID/CHID handling
- Resolve bug around nil config data
- Report version
- Add unit tests
- Fixes around library moves
- Only build libmemcached for deployments
