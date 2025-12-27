# Redis Stream Client Go - Codebase Overview

This document summarizes the structure and main concepts of the `redis-stream-client-go` repository.

## Overview

`redis-stream-client-go` provides a recoverable Redis Stream client built on top of [go-redis](https://github.com/redis/go-redis) and [redsync](https://github.com/go-redsync/redsync). The library handles consumer failures by notifying other clients and enabling them to claim work left behind by stalled or crashed consumers.

The client works alongside a **load balancer stream (LBS)** that distributes data stream names to consumers. Clients receive notifications about new streams, stream expiry, and disown events, ensuring work is rebalanced when failures occur.

## Code Layout

- **`impl/`** – Implementation of the recoverable client.
- **`impl/broker.go`** – NotificationBroker for unified output channel management.
- **`notifs/`** – Notification types for LBS and keyspace events.
- **`types/`** – Constants and public interfaces.
- **`test/`** – Integration tests using Testcontainers and Redis.
- **`imgs/`** – Diagrams referenced in the README.

Key files to explore:

1. **`types/types.go`** – Defines the `RedisStreamClient` interface with methods `Init`, `Claim`, `Done`, and `ID`.
2. **`impl/relredis.go`** – Implements the interface through `RecoverableRedisStreamClient`, managing connections, locks, and notifications.
3. **`impl/broker.go`** – Implements `NotificationBroker` for safe, synchronized notification delivery to the output channel.
4. **`impl/init.go`** – Handles keyspace notification subscriptions and the LBS reading loop.
5. **`notifs/relredisnotif.go`** – Defines notification structures such as `StreamAdded`, `StreamDisowned`, and `StreamExpired`, and the `LBSInputMessage` structure.
6. **`notifs/lbsmsg.go`** – Contains `LBSInfo` structure and helper functions for managing LBS message metadata.
7. **`test/client_test.go`** – Contains extensive integration tests demonstrating expected behaviors.

## Architecture

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

## Usage Basics

1. Create the client with your `redis.UniversalClient` and service name.
2. Call `Init(ctx)` to start listening for new streams and keyspace events. The returned channel provides unified notifications.
3. Add messages to the LBS using `LBSInputMessage` structure with `DataStreamName` and optional `Info` metadata.
4. Process notifications from the output channel, handling `StreamAdded`, `StreamExpired`, and `StreamDisowned` events.
5. Use `Claim(ctx, payload)` when receiving a stream expiration notification to take over work from stalled consumers.
6. Call `Done()` on shutdown to release locks and clean up.

## Message Structure

The library uses a standardized message format for LBS communication:

- **`LBSInputMessage`** – Structure for adding messages to LBS with `DataStreamName` and `Info` fields
- **`LBSInfo`** – Internal structure containing `DataStreamName` and `IDInLBS` for stream identification
- **`RecoverableRedisNotification`** – Notification structure with `Type`, `Payload` (LBSInfo), and `AdditionalInfo` (from original Info field)

## Internal Components

### NotificationBroker Methods

- **`Send(ctx, notification)`** – Thread-safe send to output channel; returns error if broker is closed or context cancelled
- **`Close()`** – Initiates graceful shutdown, stops accepting new sends
- **`Wait()`** – Blocks until all queued notifications are drained

### Shutdown Sequence

```go
broker.Close()          // 1. Stop accepting new sends
broker.Wait()           // 2. Block until notifications drain
close(outputChannel)    // 3. Safe to close - no more writers
```

## Next Steps for Learning

- **Redis Streams & Consumer Groups** – Review commands like `XREADGROUP`, `XPENDING`, and `XCLAIM` to understand the underlying mechanisms.
- **Distributed Locks with Redsync** – Look at `startExtendingKey` to see how the client maintains ownership via a distributed mutex.
- **NotificationBroker Pattern** – Study `impl/broker.go` to understand how concurrent notification sources are synchronized.
- **Run the Tests** – The tests under `test/` showcase how consumers coordinate and recover from failures.
- **Environment Integration** – Set up environment variables such as `POD_NAME` or `POD_IP` to ensure unique consumer IDs.
- **Read the Diagrams** – The diagrams in `imgs/` illustrate normal operation and recovery scenarios.
