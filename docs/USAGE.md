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

In `ClusterModeOSS` the client learns that a consumer's lock has expired from keyspace notifications.
Redis publishes those only from the master that holds the key, so `Init` enables
`notify-keyspace-events` on **every master** and subscribes to each of them. That set-up goes stale
when the set of masters changes, and `ReinitTopology` redoes it against the cluster's current
topology: it re-enables keyspace notifications on the current masters and rebuilds the subscriptions
on them. It does not change the cluster's topology, and it does not wait or retry.

### When to call `ReinitTopology`

Call it whenever the **set of masters** changes, or a master lost its keyspace config:

| What happened | Call it? | Why |
|---|---|---|
| Failover: a replica became master (automatic or manual) | **Yes** | The new master has no subscription, and config set with `CONFIG SET` is per node and is not replicated to it. |
| A master was added (scale out) | **Yes**, once it has joined and before slots are moved to it | Keys that land on it must be covered from the start. |
| A master was removed (scale in) | **Yes**, after it is gone | Drops the subscription to a node that no longer exists. |
| A master was restarted and its config is not persisted | **Yes** | A restart discards config set at runtime; the node then publishes nothing. |
| The old master of a failover comes back | No | It rejoins as a replica of the new master, so the set of masters did not change when it returned. The failover was the event. |
| A replica was added, removed or restarted | No | Replicas do not publish `expired` events. |
| Resharding between masters that are already subscribed | No | See [Resharding](#resharding) below. |

If a failed-over master is later promoted again (a failback), that is another failover: call it again.

What the first two rows rest on, as observed on Redis 7.0.15: only the master published the `expired`
event for a key, and a replica published none; and a node restarted without the config in
`redis.conf` came back with `notify-keyspace-events` empty.

**If you do not call it,** expiry notifications from a master the client is not subscribed to (or
that has no config) are not delivered, so no `StreamExpired` arrives for locks held there. Nothing is
lost: the periodic reconciliation scan remains the authoritative recovery path and picks those streams
up after `MinIdleTime` plus up to one `ReconciliationInterval`. Skipping the call costs recovery
latency, not data.

**Persist the config on every node.** The library applies `notify-keyspace-events KEx` with
`CONFIG SET`, which a restart discards. Put `notify-keyspace-events KEx` in `redis.conf` as well. Then
create the client with `impl.WithForceConfigOverride()`, because `Init` refuses to start when a node
already has a non-empty value and the option is not set (`errs.ErrExistingConfigWithoutOverride`);
`ReinitTopology` does not apply that check.

### How to call it

The library has no topology-change event, so trigger the call from whatever tells you the cluster
changed: your deployment or failover tooling, an alert, or a periodic timer if nothing else is
available. A successful call closes and reopens the per-master subscriptions, so avoid calling it in a
tight loop.

It first checks that the cluster is **settled**. If it is not, it returns an error wrapping
`errs.ErrClusterNotSettled` and leaves the client untouched, so retry with a delay:

```go
err := client.ReinitTopology(ctx)
switch {
case err == nil:
    // subscribed to the current masters
case errors.Is(err, errs.ErrClusterNotSettled):
    // the nodes do not agree on the topology yet (typically right after a failover); retry shortly
default:
    log.Error("reinitializing for the new topology failed", "error", err)
}
```

### What "settled" means

It is deliberately a narrow, terminal check, built from the two signals Redis itself offers:

- every node that answers reports `cluster_state:ok` in `CLUSTER INFO`, and
- all of those nodes report the same slot ownership in `CLUSTER NODES` (the "all nodes agree about
  slots configuration" check that `redis-cli --cluster check` performs).

Nodes that cannot be reached are left out of the comparison (after an automatic failover the old
master is typically down); if none can be reached at all you get a connection error instead. Epochs
and replica details are not examined. Redis has no single command that says "all nodes have
converged", so this is the check, not a guarantee against every transient state. It is a no-op in
`ClusterModeSingleShard`.

### Resharding

Resharding moves keys, with their TTLs, between masters, and an expiry is published by whichever
master holds the key when it expires. Because the client is subscribed to every master, it keeps
receiving them, so a slot migration between masters you already subscribe to does not need a
`ReinitTopology`, and an in-progress migration does **not** make the cluster unsettled for it.

This was tested rather than assumed: moving one slot holding 1500 keys that expire over 1 to 7 seconds
between two masters, while the migration ran for about 3 seconds, delivered all 1500 expiry events in
each of 3 runs, with none lost or duplicated (about 980 came from the target for keys that had been
moved, about 520 from the source for keys that expired before they were). That was one slot between
two masters of a local 3-master cluster on Redis 7.0.15 without other load. Keep in mind:

- It relies on the set of masters not changing. A master added for the resharding must be subscribed
  first (see the table above).
- Pub/Sub is at-most-once: a dropped subscription connection loses events. The reconciliation scan
  is what guarantees recovery, with or without resharding.

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

### Shutdown and the Redis connection

`Done()` cancels the client's context but does **not** close the Redis client you passed in; you
own it. The consumer's blocking read on the LBS stream (`XREADGROUP ... BLOCK 0`) is not interrupted
by canceling the context: it stays parked on the Redis server until its connection is closed.

If a new LBS message arrives while a stopped consumer's read is still parked, Redis can hand the
message to it (Redis serves blocked clients in the order they started blocking, so this is most
likely for a consumer that has been waiting the longest). Nothing processes that message. It sits in
the stopped consumer's pending list until the reconciliation scan notices it, which takes up to
`RecoveryConfig.MinIdleTime` plus one `ReconciliationInterval` (about 30-90 seconds with the
defaults), and it uses up one of the message's `RecoveryConfig.MaxRetries`. It is never processed
twice, and it is recovered without intervention.

This does not happen when a process dies (SIGKILL, OOM, pod eviction), because the operating system
closes its connections. It only applies to a consumer that stops while its Redis connection stays
open, for example while a process finishes its shutdown grace period or when consumers are stopped
and started inside a long-running process.

To avoid it, close the Redis client once `Done()` returns:

```go
<-sigChan
client.Done(ctx)
redisClient.Close() // closes the connection, which ends the parked read
```

If the Redis client is shared with other code and cannot be closed, give each stream client its own
Redis client, or accept the bounded delay described above.

## Client ID

Get consumer ID for logging:

```go
id := client.ID()  // e.g., "redis-consumer-my-pod-name"
```

## Redis Prerequisites

Enable keyspace notifications:

```bash
redis-cli CONFIG SET notify-keyspace-events KEx
```

Or in `redis.conf`:
```
notify-keyspace-events KEx
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
