# Metrics

The Simple Push server emits the following metrics:

## Client API

| Metric                          | Type    | Description                                              |
|---------------------------------|---------|----------------------------------------------------------|
| `update.client.connections`     | Gauge   | The number of open WebSocket connections.                |
| `client.socket.connect`         | Counter | WebSocket connection established.                        |
| `client.socket.disconnect`      | Counter | WebSocket connection closed.                             |
| `client.socket.lifespan`        | Timer   | The WebSocket connection duration.                       |
| `updates.client.hello`          | Counter | Client handshake complete; device ID assigned to client. |
| `updates.client.ack`            | Counter | Client acknowledged flushed updates.                     |
| `updates.client.register`       | Counter | Client subscribed to a new channel.                      |
| `updates.client.unregister`     | Counter | Client unsubscribed from an existing channel.            |
| `client.flush`                  | Timer   | The time taken to fetch and flush all pending updates.   |
| `updates.sent`                  | Counter | Pending updates flushed to client.                       |
| `updates.client.ping`           | Counter | Client sent a ping packet.                               |
| `updates.client.too_many_pings` | Counter | Client exceeded ping packet limit for this window.       |

## Application Server API

| Metric                       | Type    | Description                                                                                                                                                      |
|------------------------------|---------|------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `endpoint.socket.connect`    | Counter | Endpoint listener accepted incoming TCP connection.                                                                                                              |
| `endpoint.socket.disconnect` | Counter | Endpoint listener connection closed.                                                                                                                             |
| `updates.appserver.invalid`  | Counter | Wrong HTTP method for incoming update; error parsing update version; update URL missing primary key; error decoding primary key; primary key missing channel ID. |
| `updates.appserver.toolong`  | Counter | Incoming update payload too large.                                                                                                                               |
| `updates.appserver.incoming` | Counter | Preparing to route or deliver valid incoming update.                                                                                                             |
| `updates.appserver.received` | Counter | Update sent via the proprietary ping mechanism; or the device is connected to this node and the update was flushed via the WebSocket connection.                 |
| `updates.appserver.error`    | Counter | Failed to store update version in the backing store.                                                                                                             |
| `updates.routed.outgoing`    | Counter | Device not connected to this node; broadcasting update to other nodes.                                                                                           |
| `updates.handled`            | Timer   | The total time taken to process and successfully deliver an incoming update. This metric is not emitted if an error occurs or the device is offline.             |


## Broadcast Router

| Metric                     | Type    | Description                                                                                                                                                                                            |
|----------------------------|---------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `router.socket.connect`    | Counter | Internal routing listener accepted an incoming TCP connection from a peer. All connections use TCP keep-alive; excessive connects and disconnects indicate peers are not reusing connections properly. |
| `router.socket.disconnect` | Counter | Connection to routing listener closed by peer.                                                                                                                                                         |
| `updates.routed.invalid`   | Counter | Wrong HTTP method for routed update; malformed update envelope; update envelope missing channel ID.                                                                                                    |
| `updates.routed.unknown`   | Counter | Routing URL missing device ID; device not connected to this node.                                                                                                                                      |
| `updates.routed.incoming`  | Counter | Preparing to flush routed update to connected client.                                                                                                                                                  |
| `updates.routed.error`     | Counter | Error flushing routed update.                                                                                                                                                                          |
| `updates.routed.received`  | Counter | Successfully flushed routed update.                                                                                                                                                                    |
| `router.broadcast.error`   | Counter | * Discovery service not configured. * Error fetching peers from discovery service. * Error routing update to peers.                                                                                    |
| `router.broadcast.hit`     | Counter | Update accepted by a peer for delivery.                                                                                                                                                                |
| `router.broadcast.miss`    | Counter | Update not accepted by any peer; the device is offline.                                                                                                                                                |
| `updates.routed.hits`      | Timer   | The total time taken for a routed update to be accepted by a peer.                                                                                                                                     |
| `updates.routed.misses`    | Timer   | The time taken to determine that a routed update cannot be accepted by any peer.                                                                                                                       |
| `router.handled`           | Timer   | The time taken to broadcast an update to all nodes in a cluster.                                                                                                                                       |
| `router.dial.error`        | Counter | Peer rejected routing listener connection.                                                                                                                                                             |
| `router.dial.success`      | Counter | Peer accepted routing listener connection.                                                                                                                                                             |

## Proprietary Pinger

| Metric             | Type    | Description                    |
|--------------------|---------|--------------------------------|
| `ping.gcm.retry`   | Counter | Retrying failed GCM request.   |
| `ping.gcm.error`   | Counter | Error sending GCM request.     |
| `ping.gcm.success` | Counter | GCM request sent successfully. |

## Discovery Service

| Metric                        | Type    | Description                                  |
|-------------------------------|---------|----------------------------------------------|
| `locator.etcd.error`          | Counter | Maximum etcd operation retry count exceeded. |
| `locator.etcd.retry.request`  | Counter | Retrying failed etcd operation.              |
| `locator.etcd.retry.register` | Counter | Retrying failed etcd registration request.   |
| `locator.etcd.retry.fetch`    | Counter | Retrying failed etcd contact list request.   |

## Balancers

| Metric                     | Type    | Description                                                    |
|----------------------------|---------|----------------------------------------------------------------|
| `balancer.fetch.retry`     | Counter | Retrying request for free connection counts.                   |
| `balancer.fetch.error`     | Counter | Error fetching free connection counts from etcd.               |
| `balancer.fetch.success`   | Counter | Successfully fetched free connection counts.                   |
| `balancer.publish.retry`   | Counter | Retrying publishing this node's free connection count to etcd. |
| `balancer.publish.error`   | Counter | Error publishing free connection count to etcd.                |
| `balancer.publish.success` | Counter | Successfully published this node's free connection count.      |
| `balancer.etcd.error`      | Counter | Maximum etcd operation retry count exceeded.                   |
| `balancer.etcd.retry`      | Counter | Retrying failed etcd operation.                                |
