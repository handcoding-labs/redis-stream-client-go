# Usage Guide

## Installation

```bash
go get github.com/handcoding-labs/redis-stream-client-go
```

## Environment Variables

The client requires one of the following for unique consumer IDs:

| Variable | Description |
|----------|-------------|
| `POD_NAME` | Kubernetes pod name (preferred) |
| `POD_IP` | Pod IP address (fallback) |

```bash
export POD_NAME=my-consumer-$(hostname)-$(date +%s)
# OR
export POD_IP=$(hostname -I | awk '{print $1}')
```

Consumer ID is prefixed with `redis-consumer-` automatically.

## Creating the Client

```go
import rsc "github.com/handcoding-labs/redis-stream-client-go/impl"

client, err := rsc.NewRedisStreamClient(redisClient, "my-service")
if err != nil {
    log.Fatal(err)
}
```

### Configuration Options

```go
client, err := impl.NewRedisStreamClient(
    redisClient, 
    "my-service",
    impl.WithLBSIdleTime(30*time.Second),        // Default: 40s
    impl.WithLBSRecoveryCount(500),              // Default: 1000
    impl.WithRetryConfig(impl.RetryConfig{
        MaxRetries:        -1,                   // Default: 5
        InitialRetryDelay: 100*time.Millisecond, // Default: 100 * time.Millisecond
        MaxRetryDelay:     30*time.Second,       // Default: 30 * time.Second
    }),
)
```

| Option | Description | Default |
|--------|-------------|---------|
| `WithLBSIdleTime(d)` | Time before message considered idle | 40s |
| `WithLBSRecoveryCount(n)` | Messages to fetch during recovery | 1000 |
| `WithRetryConfig(config)` | Configure retry behavior (see below) | 5 retries, 100ms-30s backoff |

**Notes:**
- `LBSIdleTime` must be > 2× heartbeat interval (minimum 4s)
- Retry logic uses exponential backoff: 100ms → 200ms → 400ms → 800ms → ... (capped at `MaxRetryDelay`)
- Resets error counter after successful reads
- `MaxRetries = -1` => unlimited retries (recommended for production)  
             `= 0` => fail immediately (not recommended)  
             `> 0` = specific number of retry attempts

## Initialization

```go
outputChan, err := client.Init(ctx)
if err != nil {
    log.Fatal(err)
}
```

Returns a channel that receives notifications about stream events.

## Notification Types

| Type | When | Action |
|------|------|--------|
| `StreamAdded` | New stream assigned via LBS | Start processing the stream |
| `StreamExpired` | Another consumer's lock expired | Call `Claim()` to take ownership |
| `StreamDisowned` | Lost lock (was stuck too long) | Stop processing, cleanup |
| `StreamTerminated` | Channel closing | Shutdown handler |

### Handling Notifications

```go
for notification := range outputChan {
    switch notification.Type {
    case notifs.StreamAdded:
        // New stream assigned to this consumer
        go processStream(notification.Payload.DataStreamName)
        
    case notifs.StreamExpired:
        // Another consumer died, try to claim their stream
        if err := client.Claim(ctx, notification.Payload); err != nil {
            log.Warn("Claim failed", "error", err)
        } else {
            go processStream(notification.Payload.DataStreamName)
        }
        
    case notifs.StreamDisowned:
        // We lost ownership (were stuck too long)
        cancelProcessing(notification.Payload.DataStreamName)
        
    case notifs.StreamTerminated:
        // Channel closing, shutdown
        log.Info("Shutting down", "reason", notification.AdditionalInfo["info"])
    }
}
```

### Notification Payload

```go
type LBSInfo struct {
    DataStreamName string // Name of the data stream
    IDInLBS        string // Message ID in Load Balancer Stream
}
```

`AdditionalInfo` map contains metadata from the original `LBSInputMessage.Info`.

## Adding Messages to LBS

Producers add streams to the LBS for distribution:

```go
import "github.com/handcoding-labs/redis-stream-client-go/notifs"

lbsMessage := notifs.LBSInputMessage{
    DataStreamName: "user-session-123",
    Info: map[string]interface{}{
        "user_id":  "user-456",
        "priority": "high",
    },
}

messageData, _ := json.Marshal(lbsMessage)
redisClient.XAdd(ctx, &redis.XAddArgs{
    Stream: "my-service-input",  // <service_name>-input
    Values: map[string]interface{}{
        "lbs-input": string(messageData),
    },
})
```

## Claiming Expired Streams

When `StreamExpired` notification arrives:

```go
case notifs.StreamExpired:
    if err := client.Claim(ctx, notification.Payload); err != nil {
        // Another consumer got there first - expected behavior
        log.Debug("Claim failed", "error", err)
    } else {
        log.Info("Claimed stream", "stream", notification.Payload.DataStreamName)
        go processStream(notification.Payload.DataStreamName)
    }
```

Claim failures are normal—multiple consumers race to claim expired streams.

## Completing Stream Processing

After processing a stream:

```go
err := client.DoneStream(ctx, streamName)
```

This:
- Unlocks the distributed lock
- Acknowledges the LBS message
- Cleans up internal state

**Important:** Always call `DoneStream()` when done. Failing to do so causes:
- Lock to expire (other consumers claim it)
- Memory leak (goroutine keeps running)
- Redis memory growth

## Client Shutdown

```go
err := client.Done()
```

This:
- Calls `DoneStream()` for all active streams
- Drains pending notifications
- Closes channels and cancels contexts

### Graceful Shutdown Pattern

```go
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

go func() {
    for notification := range outputChan {
        // handle notifications
    }
}()

<-sigChan
client.Done()
```

## Client ID

Get consumer ID for logging:

```go
id := client.ID()  // e.g., "redis-consumer-my-pod-name"
```

## Redis Prerequisites

Enable keyspace notifications:

```bash
redis-cli CONFIG SET notify-keyspace-events Ex
```

Or in `redis.conf`:
```
notify-keyspace-events Ex
```
