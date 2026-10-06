# Redis Stream Client Go - Codebase Overview

This document summarizes the structure and main concepts of the `redis-stream-client-go` repository.

## Overview

`redis-stream-client-go` provides a recoverable Redis Stream client built on top of [go-redis](https://github.com/redis/go-redis) and [redsync](https://github.com/go-redsync/redsync). The library handles consumer failures by notifying other clients and enabling them to claim work left behind by stalled or crashed consumers.

The client works alongside a **load balancer stream (LBS)** that distributes data stream names to consumers. Clients receive notifications about new streams, stream expiry, and disown events, ensuring work is rebalanced when failures occur.

## Code Layout

- **`metrics/`** – Defines `Recorder` interface and default implementations (noop, test).  Enables instrumentation of client operations.
- **`impl/`** – Implementation of the recoverable client.
- **`notifs/broker.go`** – NotificationBroker for unified output channel management.
- **`notifs/`** – Notification types for LBS and keyspace events.
- **`configs/`** – Constants and tunable defaults.
- **`types/`** – Public interfaces and error types (`types/errs`).
- **`test/`** – Integration tests using Testcontainers and Redis.
- **`imgs/`** – Diagrams referenced in the README.

Key files to explore:

*Examples*: a Prometheus recorder lives in `examples/prometheus/recorder.go` which demonstrates how the
`metrics.Recorder` can be implemented for real‑world monitoring.


1. **`types/types.go`** – Defines the `RedisStreamClient` interface with methods `Init`, `Claim`, `Done`, `DoneStream`, `ResetTopology`, and `ID`.
2. **`impl/relredis.go`** – Implements the interface through `RecoverableRedisStreamClient`, managing connections, locks, notifications, `Claim` (re-queue) and `ResetTopology`.
3. **`impl/opts.go`** – Functional options plus the `RecoveryConfig` and `ClusterMode` types.
4. **`impl/init.go`** – Keyspace subscription (single-shard / all-masters), the LBS reading loop, and the periodic reconciliation scan (`runReconciliationLoop` / `reconcileLBS`).
5. **`impl/helpers.go`** – The shared `reQueue` primitive (lock-liveness check, XACK-first dedup, XADD re-queue, DLQ routing) and retry-count/jitter helpers.
6. **`notifs/broker.go`** – Implements `NotificationBroker` for safe, synchronized notification delivery to the output channel.
7. **`notifs/relredisnotif.go`** – Defines notification structures such as `StreamAdded`, `StreamDisowned`, and `StreamExpired`, and the `LBSInputMessage` structure.
8. **`notifs/lbsmsg.go`** – Contains `LBSInfo` structure and helper functions for managing LBS message metadata.
9. **`test/client_test.go`** and **`test/cluster_test.go`** – Integration tests demonstrating expected behaviors, including recovery, dedup, DLQ routing, and cluster mode.

## Architecture

### Threading Model

The library uses a multi-goroutine architecture:

```
Per Client Instance:
├── 1 × LBS Reader            - Blocking read on Load Balancer Stream
├── 1 × Keyspace Listener     - Redis pub/sub for key expirations (one per master in OSS mode)
├── 1 × Reconciliation Scanner - Periodic XPENDING scan that re-queues dead consumers' work
├── 1 × Notification Broker   - Serializes notifications to output
└── N × Key Extenders         - One per active stream (lock heartbeats)

Total: 4 + N goroutines (where N = number of active streams; +1 per extra master in OSS mode)
```

**Goroutine lifecycle:**
- LBS Reader and Keyspace Listener live for the client's lifetime
- Key Extenders spawn when a stream is assigned, exit on `DoneStream()` or lock failure
- All goroutines clean up on `Done()` or context cancellation

### NotificationBroker

The `NotificationBroker` is a key internal component that provides safe, synchronized access to the output notification channel. Multiple goroutines need to send notifications:

- **`startExtendingKey`**: Key extenders running one per stream
- **`listenToKsp`**: Listens to Redis for pub/sub keyspace notifications
- **`readLBSStream`**: Perpetually reads from the Load Balancer Stream

The broker pattern ensures:
- Thread-safe writes to the output channel
- No panics on send to closed channels
- Graceful shutdown with notification draining
- Unified error handling across all notification sources

```
┌─────────────────────┐     ┌─────────────────────┐
│  startExtendingKey  │────▶│                     │
└─────────────────────┘     │                     │
                            │  NotificationBroker │────▶ outputChan ────▶ Consumer
┌─────────────────────┐     │                     │
│     listenToKsp     │────▶│                     │
└─────────────────────┘     │                     │
                            │                     │
┌─────────────────────┐     │                     │
│   readLBSStream     │────▶│                     │
└─────────────────────┘     └─────────────────────┘
```

### Backpressure Handling

When consumer processing is slower than message arrival:

1. Output channel buffer fills (500 notifications)
2. NotificationBroker blocks on send
3. Upstream goroutines block waiting for broker
4. Messages accumulate in Redis pending entries list

**Mitigation:** Process notifications concurrently using worker pools.

### Memory Model

| Component | Memory | Scales With |
|-----------|--------|-------------|
| Base channels | ~300 KB | Fixed per client |
| Per-stream overhead | ~2.5 KB | Active stream count |
| Goroutine stacks | ~2 KB each | 4 + active streams |

**Example:** 100 active streams ≈ 550 KB total memory

## Error Handling

The library now uses a combination of sentinel errors and wrapped errors for better granularity and consistency. This approach ensures:

- **Sentinel Errors**: Predefined constants for common error types, allowing easy comparison and handling.
- **Wrapped Errors**: Contextual information added to errors using `fmt.Errorf` with the `%w` verb, enabling error unwrapping and detailed debugging.

### Example

```go
if errors.Is(err, rediserr.ErrStreamNotFound) {
    log.Warn("Stream not found", "stream", streamName)
} else {
    log.Error("Unexpected error", "error", err)
}
```

## Next Steps for Learning

- **Redis Streams & Consumer Groups** – Review commands like `XREADGROUP`, `XPENDING`, `XACK`, and `XADD` to understand the underlying mechanisms (recovery re-queues via `XACK` + `XADD`).
- **Distributed Locks with Redsync** – Look at `startExtendingKey` to see how the client maintains ownership via a distributed mutex, and `reQueue` for the lock-liveness check.
- **NotificationBroker Pattern** – Study `notifs/broker.go` to understand how concurrent notification sources are synchronized.
- **Run the Tests** – The tests under `test/` showcase how consumers coordinate and recover from failures.
- **Environment Integration** – Set up environment variables such as `POD_NAME` or `POD_IP` to ensure unique consumer IDs.
- **Read the Diagrams** – The diagrams in `imgs/` illustrate normal operation and recovery scenarios.
