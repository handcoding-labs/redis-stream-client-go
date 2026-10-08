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
import (
    "log/slog"
    rsc "github.com/handcoding-labs/redis-stream-client-go/impl"
)

client, err := rsc.NewRedisStreamClient(
    redisClient,
    "my-service",
    rsc.WithClusterMode(rsc.ClusterModeSingleShard), // or ClusterModeOSS (needs *redis.ClusterClient)
    rsc.WithRecoveryConfig(rsc.RecoveryConfig{
        ReconciliationInterval: 60 * time.Second,    // Default: 60s
        MinIdleTime:            30 * time.Second,    // Default: 30s
        BatchSize:              50,                  // Default: 50
        MaxRetries:             3,                   // Default: 3
        DLQStream:              "my-service-dlq",    // Default: "" => "<service>-input-dlq"
        DLQMaxLen:              10000,               // Default: 10000 (approx MAXLEN); 0 => unbounded
    }),
    rsc.WithRetryConfig(rsc.RetryConfig{
        MaxRetries:        -1,                   // Default: 5
        InitialRetryDelay: 100*time.Millisecond, // Default: 100 * time.Millisecond
        MaxRetryDelay:     30*time.Second,       // Default: 30 * time.Second
    }),
    rsc.WithLogger(slog.New(customHandler)),    // Optional: custom logger
)
```

| Option | Description | Default |
|--------|-------------|---------|
| `WithClusterMode(m)` | `ClusterModeSingleShard` or `ClusterModeOSS` (Redis Cluster) | SingleShard |
| `WithRecoveryConfig(c)` | Tunes the periodic reconciliation scan (interval, min idle, batch, retries, DLQ) | 60s / 30s / 50 / 3 / "" |
| `WithRetryConfig(config)` | Configure LBS-read retry behavior (see below) | 5 retries, 100ms-30s backoff |
| `WithLogger(logger)` | Custom slog.Logger implementation | slog.Default() |
| `WithMetricsRecorder(recorder)` | Provide your own `metrics.Recorder` implementation for instrumentation | &metrics.NoopRecorder{} |

> **Deprecated:** `WithLBSIdleTime` and `WithLBSRecoveryCount` no longer affect recovery (now governed by `RecoveryConfig`) and will be removed in a future release. `ClusterModeOSS` requires the underlying client to be a `*redis.ClusterClient`; recovery requires **Redis 6.2+**.

**Notes:**
- `MinIdleTime` should be comfortably larger than the heartbeat interval so a live consumer's lock is always present before its message becomes eligible for recovery.
- Retry logic uses exponential backoff: 100ms → 200ms → 400ms → 800ms → ... (capped at `MaxRetryDelay`)
    - Resets error counter after successful reads
    - `MaxRetries = -1` => unlimited retries (recommended for production)  
             `= 0` => fail immediately (not recommended)  
             `> 0` = specific number of retry attempts
- Logger defaults to `slog.Default()` which writes to `stderr`. Use `WithLogger()` to provide custom logging handler (e.g., for Cloud Logging, JSON formatting, etc.)
- **Metrics:** to collect operational metrics, pass a recorder via `WithMetricsRecorder`.  A Prometheus implementation is included under
  `examples/prometheus`; see [docs/METRICS.md](METRICS.md) for full details.

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
| `StreamAdded` | A stream is assigned to this consumer (fresh or recovered/re-queued) | Start processing the stream |
| `StreamExpired` | A keyspace notification reports another consumer's lock expired | Optionally call `Claim()` to re-queue it (low-latency fast path); do not process here |
| `StreamDisowned` | Lost lock (was stuck too long) | Stop processing, cleanup |
| `StreamTerminated` | Channel closing | Shutdown handler |

### Handling Notifications

```go
for notification := range outputChan {
    switch notification.Type {
    case notifs.StreamAdded:
        // Stream assigned to this consumer (fresh, or recovered and re-queued)
        go processStream(notification.Payload.DataStreamName)
        
    case notifs.StreamExpired:
        // Another consumer died: re-queue its stream for redistribution. Do NOT process here —
        // the re-queued stream arrives as StreamAdded when picked up. Handling this is optional;
        // the periodic reconciliation scan recovers it regardless.
        if err := client.Claim(ctx, notification.Payload); err != nil {
            log.Debug("stream already recovered elsewhere", "error", err)
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

`Claim` recovers an expired stream by acknowledging the dead consumer's pending message and
re-adding it to the LBS as a new message (`XACK` + `XADD`). It does **not** grant ownership to the
caller — the re-queued stream is redistributed normally and the consumer that picks it up (possibly
this one) receives a `StreamAdded`. Process the stream then, not right after `Claim`.

```go
case notifs.StreamExpired:
    // Trigger recovery; processing happens on the subsequent StreamAdded.
    if err := client.Claim(ctx, notification.Payload); err != nil {
        // ErrAlreadyClaimed: another consumer or the reconciliation scan already recovered it.
        log.Debug("already recovered", "error", err)
    }
```

A non-nil error (`errs.ErrAlreadyClaimed`) is normal — multiple consumers and the periodic scan can
race to recover the same stream; only one wins. You may also skip handling `StreamExpired` entirely
and rely solely on the periodic reconciliation scan (required in multi-shard clusters).

## Cluster Topology Changes (ClusterModeOSS)

In `ClusterModeOSS`, call `ResetTopology` after a failover or resharding to reload the cluster view
and rebuild keyspace subscriptions on the current set of masters:

```go
if err := client.ResetTopology(ctx); err != nil {
    log.Error("topology reset failed", "error", err)
}
```

It is a no-op in `ClusterModeSingleShard`.

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
err := client.Done(ctx)
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
client.Done(ctx)
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

## Error Handling

The client uses sentinel and wrapped errors to provide detailed error information. Use `errors.Is` to check for specific sentinel errors and `errors.Unwrap` to retrieve the underlying error.

### Example

```go
if errors.Is(err, rediserr.ErrStreamNotFound) {
    log.Warn("Stream not found", "stream", streamName)
} else if unwrappedErr := errors.Unwrap(err); unwrappedErr != nil {
    log.Error("Underlying error", "error", unwrappedErr)
} else {
    log.Error("Unexpected error", "error", err)
}
```
