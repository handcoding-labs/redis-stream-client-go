# Redis Stream Client Go - Codebase Overview

This document summarizes the structure and main concepts of the `redis-stream-client-go` repository.

## Overview

`redis-stream-client-go` provides a recoverable Redis Stream client built on top of [go-redis](https://github.com/redis/go-redis) and [redsync](https://github.com/go-redsync/redsync). The library handles consumer failures by notifying other clients and enabling them to claim work left behind by stalled or crashed consumers.

The client works alongside a **load balancer stream (LBS)** that distributes data stream names to consumers. Clients receive notifications about new streams, stream expiry, and disown events, ensuring work is rebalanced when failures occur.

## Code Layout

- **`impl/`** – Implementation of the recoverable client.
- **`notifs/`** – Notification types for LBS and keyspace events.
- **`types/`** – Constants and public interfaces.
- **`test/`** – Integration tests using Testcontainers and Redis.
- **`imgs/`** – Diagrams referenced in the README.

Key files to explore:

1. **`types/types.go`** – Defines the `RedisStreamClient` interface with methods `Init`, `Claim`, `Done`, and `ID`.
2. **`impl/relredis.go`** – Implements the interface through `RecoverableRedisStreamClient`, managing connections, locks, and notifications.
3. **`impl/init.go`** – Handles keyspace notification subscriptions and the LBS reading loop.
4. **`notifs/relredisnotif.go`** – Defines notification structures such as `StreamAdded`, `StreamDisowned`, and `StreamExpired`, and the `LBSInputMessage` structure.
5. **`notifs/lbsmsg.go`** – Contains `LBSInfo` structure and helper functions for managing LBS message metadata.
6. **`test/client_test.go`** – Contains extensive integration tests demonstrating expected behaviors.

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

## Next Steps for Learning

- **Redis Streams & Consumer Groups** – Review commands like `XREADGROUP`, `XPENDING`, and `XCLAIM` to understand the underlying mechanisms.
- **Distributed Locks with Redsync** – Look at `startExtendingKey` to see how the client maintains ownership via a distributed mutex.
- **Run the Tests** – The tests under `test/` showcase how consumers coordinate and recover from failures.
- **Environment Integration** – Set up environment variables such as `POD_NAME` or `POD_IP` to ensure unique consumer IDs.
- **Read the Diagrams** – The diagrams in `imgs/` illustrate normal operation and recovery scenarios.

