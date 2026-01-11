# Architecture

## Concept

Redis streams are typically used for data written at one end and consumed at the other.

![Redis streams normal working](../imgs/redis_stream_normal.png)

When consumers fail (crash or get stuck), recovery uses XCLAIM/XAUTOCLAIM. This requires stateful consumers that know their identity via machine name or IP.

![Redis streams failure recovery](../imgs/redis_stream_failure_recovery.png)

**Limitations:**
1. Recovery depends on crashed consumer restarting quickly
2. Stuck consumers (GC, stop-the-world) block processing indefinitely

This library solves both by:
1. Using keyspace notifications to inform other consumers of failures
2. Providing a Claim API for immediate takeover

![Redis streams failure recovery - new](../imgs/redis_stream_failure_recovery-redis-stream-client_way.png)

## Load Balancer Stream (LBS)

The LBS distributes incoming streams (not stream data) among consumers using Redis consumer groups with round-robin delivery.

![Redis stream client - LBS](../imgs/redis_stream_client_lbs.png)

## Threading Model

The library spawns multiple goroutines per client:

```
┌─────────────────────────────────────────────────────────────────┐
│                         Client Instance                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌─────────────────┐                                            │
│  │ LBS Reader      │  1 goroutine - reads from Load Balancer    │
│  │ (blocking read) │  Stream, assigns streams to this consumer  │
│  └─────────────────┘                                            │
│                                                                  │
│  ┌─────────────────┐                                            │
│  │ Keyspace        │  1 goroutine - listens for Redis key       │
│  │ Listener        │  expiration events (pub/sub)               │
│  └─────────────────┘                                            │
│                                                                  │
│  ┌─────────────────┐                                            │
│  │ Key Extender    │  N goroutines - one per active stream      │
│  │ (stream-1)      │  extends distributed lock every hbInterval │
│  ├─────────────────┤                                            │
│  │ Key Extender    │  Goroutines exit when:                     │
│  │ (stream-2)      │  - DoneStream() called                     │
│  ├─────────────────┤  - Lock extension fails                    │
│  │ Key Extender    │  - Context cancelled                       │
│  │ (stream-N)      │                                            │
│  └─────────────────┘                                            │
│                                                                  │
│  ┌─────────────────┐                                            │
│  │ Notification    │  1 goroutine - serializes all              │
│  │ Broker          │  notifications to output channel           │
│  └─────────────────┘                                            │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘

Total goroutines per client: 3 + N (where N = active streams)
```

**Key points:**
- Each active stream has its own key extender goroutine
- Goroutines are lightweight (~2KB stack) but scale with stream count
- All goroutines are properly cleaned up on `Done()` or context cancellation

## NotificationBroker

The library uses an internal `NotificationBroker` to safely manage notifications from multiple concurrent sources. This ensures thread-safe delivery to the output channel and prevents panics during shutdown.

```
┌─────────────────────┐     ┌─────────────────────┐
│  Key Extenders      │────▶│                     │
│  (one per stream)   │     │                     │
└─────────────────────┘     │                     │
                            │  NotificationBroker │────▶ outputChan
┌─────────────────────┐     │                     │
│  Keyspace Listener  │────▶│  - Thread-safe      │
│  (Redis pub/sub)    │     │  - Graceful shutdown│
└─────────────────────┘     │  - No send panics   │
                            │                     │
┌─────────────────────┐     │                     │
│  LBS Stream Reader  │────▶│                     │
└─────────────────────┘     └─────────────────────┘
```

### Shutdown Sequence

1. `Close()` sets closed flag and closes quit channel
2. `run()` goroutine exits select, drains remaining input messages
3. `Wait()` blocks until `run()` completes
4. Safe to close output channel—no more writers

## Redis Keys Created

| Key Pattern | Purpose | TTL |
|-------------|---------|-----|
| `<service>-input` | LBS stream | Persistent |
| `<service>-group` | Consumer group | Persistent |
| `mutex:<stream>` | Distributed lock | `hbInterval` |

## Design Decisions

**Why one goroutine per stream for lock extension?**
- Simplicity: each stream is independent
- Fault isolation: one stuck stream doesn't affect others
- Scales fine to ~1000 streams per client

**Why NotificationBroker instead of direct channel sends?**
- Multiple writers to single output channel
- Graceful shutdown without panics
- Centralized backpressure handling

**Why keyspace notifications instead of polling?**
- Lower latency on lock expiration
- No wasted Redis commands
- Native Redis pub/sub reliability
