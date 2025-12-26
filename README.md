Interactive demo: https://g.co/gemini/share/999b256cffd3

# redis-stream-client-go

[![Go](https://github.com/handcoding-labs/redis-stream-client-go/actions/workflows/go.yml/badge.svg)](https://github.com/handcoding-labs/redis-stream-client-go/actions/workflows/go.yml)

A redis stream based client that can recover from failures. This lib is based on [go-redis](https://github.com/redis/go-redis) and [redsync](https://github.com/go-redsync/redsync)

Redis streams are awesome! Typically they are used for data written in one end and consumed at other.

![Redis streams normal working](./imgs/redis_stream_normal.png)

When one (or more) of the consumers fail (crash, get stuck for abnormal period of time), the way to recover is by using XCLAIM (and XAUTOCLAIM) per redis streams. This supposes that your consumers are stateful i.e. they know who they are via a dedicated machine name or IP. This way using XPENDING and XCLAIM you can recover from failed or stuck situations.

![Redis streams failure recovery](./imgs/redis_stream_failure_recovery.png)

However, there are two requirements that it doesn't meet:
1. Recovery depends on how soon the crashed consumer can come back up and claim. This is normally a small time (few seconds) but sometimes it can be high due to startup logic.
2. When a consumer gets stuck (GC or some such stop-the-world process) then the processing is stuck.

In both situations above, there are other consumers waiting and perhaps availble who can claim and continue processing in real-time. However, due to redis' pull based mechanism they don't know if they need to.

This library aims to provide two such constructs built on top of redis' own data structures:
1. Inform other consumers that a consumer is dead or stuck via key space notifications.
2. Provide API to claim the stream being processed.

![Redis streams failure recovery - new](./imgs/redis_stream_failure_recovery-redis-stream-client_way.png)

In addition to this, for better management, the library provides a load balancer stream (LBS) based on redis streams and consumer groups that work in a load balanced fashion which can distribute incoming streams (not stream data!) among existing consumers using round-robin fashion.

![Redis stream client - LBS](./imgs/redis_stream_client_lbs.png)

# Architecture

## NotificationBroker

The library uses an internal `NotificationBroker` component to safely manage notifications from multiple concurrent sources. This ensures thread-safe delivery to the output channel and prevents panics during shutdown.

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

# usage

Just import the library:

```
go get https://github.com/handcoding-labs/redis-stream-client-go
```

Create the client:

```
import rsc "github.com/handcoding-labs/redis-stream-client-go/impl"
```

```
client := rsc.NewRedisStreamClient(<go redis client>, <service_name>)
```

## Configuration Options

The client supports optional configuration parameters:

```go
import "github.com/handcoding-labs/redis-stream-client-go/impl"

// Create client with custom configuration
client, err := impl.NewRedisStreamClient(
    redisClient, 
    "my-service",
    impl.WithLBSIdleTime(30*time.Second),        // Time before message considered idle (default: 40s)
    impl.WithLBSRecoveryCount(500),              // Number of messages to recover at once (default: 1000)
)
```

### Available Options:

- **`WithLBSIdleTime(duration)`**: Sets the time after which a message is considered idle and will be recovered by other consumers. Must be greater than 2 × heartbeat interval (4s minimum).
- **`WithLBSRecoveryCount(count)`**: Sets the number of messages to fetch at a time during recovery operations.

## Environment Variables

The client requires one of the following environment variables to generate unique consumer IDs:

- **`POD_NAME`**: Kubernetes pod name (preferred in containerized environments)
- **`POD_IP`**: Pod IP address (fallback option)

```bash
# Example setup
export POD_NAME=my-consumer-$(hostname)-$(date +%s)
# OR
export POD_IP=$(hostname -I | awk '{print $1}')
```

The consumer ID will be prefixed with `redis-consumer-` automatically.

Initialize the client and use the LBC and Key space notification channel for tracking which data streams to read and which have expired respectively:

```
outputChan, err := client.Init(ctx)
```

## Adding Messages to LBS

To add messages to the Load Balancer Stream (LBS), use the `LBSInputMessage` structure:

```go
import "github.com/handcoding-labs/redis-stream-client-go/notifs"

// Create an LBS message
lbsMessage := notifs.LBSInputMessage{
    DataStreamName: "user-session-123",
    Info: map[string]interface{}{
        "user_id":    "user-456",
        "session_id": "session-789",
        "priority":   "high",
        "created_at": time.Now().Format(time.RFC3339),
    },
}

// Marshal to JSON
messageData, err := json.Marshal(lbsMessage)
if err != nil {
    return err
}

// Add to LBS stream
result := redisClient.XAdd(ctx, &redis.XAddArgs{
    Stream: "<service_name>-input", // LBS stream name
    Values: map[string]interface{}{
        "lbs-input": string(messageData),
    },
})
```

The `Info` field in `LBSInputMessage` allows you to pass additional metadata that will be available in the notification's `AdditionalInfo` field when the stream is assigned to a consumer.

## Notification Types

There are currently four types of notifications sent on `outputChan`:
1. `StreamAdded` - When a new stream gets added to LBS. You should take the stream and start reading your data from it using standard `XREAD` or `XREADGROUP` commands as applicable. The notification includes:
   - `Payload`: Contains `DataStreamName` and `IDInLBS`
   - `AdditionalInfo`: Contains the `Info` field from the original `LBSInputMessage`
2. `StreamExpired` - When a client's ownership of stream expires and it relinquishes the lock. This is sent when key space notification arrives on stream expiry. Other clients should process this and take ownership of the stream by using `Claim` API.
3. `StreamDisowned` - When a client gets stuck (not crashed) and thus automatically relinquishes ownership, another active client will claim it. When the old client comes back, it will fail to extend the lock and thus will be informed that it now doesn't own the stream. The old client should gracefully exit by calling `Done` API.
4. `StreamTerminated` - Internal notification indicating the notification channel is closing, typically due to context cancellation or fatal errors. Contains additional info about the termination reason.

# claiming

When you receive a `StreamExpired` notification, you can claim the expired stream using the LBSInfo from the notification payload:

```go
case notifs.StreamExpired:
    if err := client.Claim(ctx, notification.Payload); err != nil {
        // Handle claim failure - another consumer may have claimed it first
        slog.Warn("Failed to claim expired stream", "error", err, "stream", notification.Payload.DataStreamName)
    } else {
        slog.Info("Successfully claimed expired stream", "stream", notification.Payload.DataStreamName)
        // Process the claimed stream
        go handleClaimedStream(ctx, notification.Payload.DataStreamName)
    }
```

An error in `Claim` indicates the client was not successful in claiming the stream as some other client got there before.

# stream lifecycle management

The library provides granular control over stream lifecycle:

## processing individual streams

After processing is done for a specific data stream, call `DoneStream` to mark the end of processing for that particular stream:

```
err := client.DoneStream(ctx, <data_stream_name>)
```

This method:
- Unlocks the distributed lock for the stream
- Acknowledges the message in the LBS stream
- Cleans up internal state for that specific stream

## client shutdown

When the client is shutting down completely, call `Done` to clean up all streams handled by the client:

```
err := client.Done()
```

This method calls `DoneStream` for all active streams and then performs additional cleanup like closing channels and canceling contexts. The internal `NotificationBroker` ensures all pending notifications are drained before the output channel is closed.

Method `ID()` can be used to obtain client ID for logging purposes:

```
client.ID()
```

# Complete Example

Here's a complete working example:

```go
package main

import (
    "context"
    "encoding/json"
    "log/slog"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/handcoding-labs/redis-stream-client-go/impl"
    "github.com/handcoding-labs/redis-stream-client-go/notifs"
    "github.com/handcoding-labs/redis-stream-client-go/types"
)

func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Set required environment variable
    os.Setenv("POD_NAME", "example-consumer")

    // Create Redis client
    redisClient := redis.NewUniversalClient(&redis.UniversalOptions{
        Addrs: []string{"localhost:6379"},
        DB:    0,
    })
    defer redisClient.Close()

    // Enable keyspace notifications
    redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex")

    // Create and initialize stream client
    client, err := impl.NewRedisStreamClient(redisClient, "example-service")
    if err != nil {
        slog.Error("could not initialize", "error", err.Error())
    }
    outputChan, err := client.Init(ctx)
    if err != nil {
        slog.Error("Failed to initialize client", "error", err)
        return
    }

    // Handle graceful shutdown
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

    // Process notifications
    go func() {
        for notification := range outputChan {
            switch notification.Type {
            case notifs.StreamAdded:
                slog.Info("New stream assigned", "stream", notification.Payload.DataStreamName)
                // Process the stream and call DoneStream when finished
                go processStream(ctx, client, notification)

            case notifs.StreamExpired:
                if err := client.Claim(ctx, notification.Payload); err != nil {
                    slog.Warn("Failed to claim stream", "error", err)
                } else {
                    slog.Info("Claimed expired stream", "stream", notification.Payload.DataStreamName)
                    go processStream(ctx, client, notification)
                }

            case notifs.StreamDisowned:
                slog.Warn("Lost stream ownership", "stream", notification.Payload.DataStreamName)
            
            case notifs.StreamTerminated:
                slog.Info("Notification channel closing", "reason", notification.AdditionalInfo["info"])
            }
        }
    }()

    // Add test message to LBS
    go addTestMessage(ctx, redisClient)

    // Wait for shutdown
    <-sigChan
    client.Done()
}

func processStream(ctx context.Context, client types.RedisStreamClient, notification notifs.RecoverableRedisNotification) {
    streamName := notification.Payload.DataStreamName
    slog.Info("Processing stream", "stream", streamName, "info", notification.AdditionalInfo)
    
    // Simulate processing
    time.Sleep(2 * time.Second)
    
    // Mark stream as done
    if err := client.DoneStream(ctx, streamName); err != nil {
        slog.Error("Failed to mark stream done", "error", err, "stream", streamName)
    } else {
        slog.Info("Stream processing completed", "stream", streamName)
    }
}

func addTestMessage(ctx context.Context, redisClient redis.UniversalClient) {
    time.Sleep(1 * time.Second) // Wait for client to be ready
    
    lbsMessage := notifs.LBSInputMessage{
        DataStreamName: "test-stream-1",
        Info: map[string]interface{}{
            "priority": "high",
            "user_id":  "user-123",
        },
    }

    messageData, _ := json.Marshal(lbsMessage)
    redisClient.XAdd(ctx, &redis.XAddArgs{
        Stream: "example-service-input",
        Values: map[string]interface{}{
            "lbs-input": string(messageData),
        },
    })
}
```

# Troubleshooting

## Common Issues

### "POD_NAME or POD_IP not set"
**Solution**: Set one of the required environment variables:
```bash
export POD_NAME=my-consumer-$(date +%s)
# OR
export POD_IP=$(hostname -I | awk '{print $1}')
```

### No notifications received
**Possible causes**:
1. **Keyspace notifications not enabled**: Ensure `notify-keyspace-events` is set to `Ex`
2. **No messages in LBS**: Check if messages are being added to the `<service-name>-input` stream
3. **Consumer group issues**: Verify the consumer group exists and is properly configured

**Debug commands**:
```bash
# Check keyspace notifications
redis-cli CONFIG GET notify-keyspace-events

# Check LBS stream
redis-cli XINFO STREAM my-service-input

# Check consumer group
redis-cli XINFO GROUPS my-service-input
```

### "Failed to claim expired stream" errors
**Cause**: Multiple consumers trying to claim the same expired stream simultaneously.
**Solution**: This is expected behavior - only one consumer can claim a stream. Handle the error gracefully.

### High memory usage
**Causes**:
1. **Streams not being acknowledged**: Ensure `DoneStream()` is called after processing
2. **Large number of pending messages**: Check with `XPENDING` command
3. **Old messages not trimmed**: Consider implementing stream trimming

**Monitoring commands**:
```bash
# Check pending messages
redis-cli XPENDING my-service-input my-service-group

# Check stream length
redis-cli XLEN my-service-input

# Check memory usage
redis-cli INFO memory
```

### Consumer appears stuck
**Possible causes**:
1. **Network issues**: Check Redis connectivity
2. **Long-running processing**: Ensure processing completes within idle time limits
3. **Deadlocks**: Review your processing logic for potential deadlocks

**Solutions**:
- Implement timeouts in your processing logic
- Use context cancellation for graceful shutdowns
- Monitor processing times and adjust `WithLBSIdleTime` if needed

### Output channel closed unexpectedly
**Cause**: The internal `NotificationBroker` detected a shutdown condition.
**Solution**: Check for `StreamTerminated` notifications which contain the reason for closure in `AdditionalInfo["info"]`. Common reasons include context cancellation or Redis connection errors.
