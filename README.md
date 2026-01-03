# redis-stream-client-go

[![Go](https://github.com/handcoding-labs/redis-stream-client-go/actions/workflows/go.yml/badge.svg)](https://github.com/handcoding-labs/redis-stream-client-go/actions/workflows/go.yml)

A Redis stream-based client with automatic failure recovery. Built on [go-redis](https://github.com/redis/go-redis) and [redsync](https://github.com/go-redsync/redsync).

## Problem

Standard Redis stream recovery via XCLAIM requires:
1. Crashed consumer to come back up and reclaim
2. Stuck consumers (GC pauses) block processing

Other available consumers can't help because Redis is pull-based—they don't know recovery is needed.

## Solution

This library provides:
1. **Keyspace notifications** - Inform other consumers when a consumer dies or gets stuck
2. **Claim API** - Allow any consumer to claim orphaned streams
3. **Load Balancer Stream (LBS)** - Distribute streams across consumers via round-robin

![Redis stream client - LBS](./imgs/redis_stream_client_lbs.png)

## Quick Start

```go
client, _ := impl.NewRedisStreamClient(redisClient, "my-service")
outputChan, _ := client.Init(ctx)

for notification := range outputChan {
    switch notification.Type {
    case notifs.StreamAdded:
        go process(notification.Payload.DataStreamName)
    case notifs.StreamExpired:
        client.Claim(ctx, notification.Payload)
    }
}
```

## Documentation

| Document | Description |
|----------|-------------|
| [Architecture](./docs/ARCHITECTURE.md) | Threading model, NotificationBroker, internal design |
| [Usage Guide](./docs/USAGE.md) | API reference, configuration, notification types |
| [Operations](./docs/OPERATIONS.md) | Memory, limits, backpressure, troubleshooting |
| [Example](./docs/EXAMPLE.md) | Complete working example |
| [Codebase Overview](./docs/CODEBASE_OVERVIEW.md) | Codebase overview |

## Installation

```bash
go get github.com/handcoding-labs/redis-stream-client-go
```

## Requirements

- Go 1.21+
- Redis 6.0+ with keyspace notifications enabled (`notify-keyspace-events Ex`)
- Environment variable: `POD_NAME` or `POD_IP`

## License

LGPL-2.1
