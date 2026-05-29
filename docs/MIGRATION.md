# Migration Guide: Redis Cluster support (#60)

This release replaces the `XCLAIM`/`XAUTOCLAIM` recovery mechanism with a cluster-safe
`XACK` + `XADD` reconciliation scan, adds OSS Redis Cluster support, and introduces a `_retry_count`
field with dead-letter-queue (DLQ) routing. This guide lists the changes you need to make.

## 1. Minimum Redis version

The reconciliation scan uses `XPENDING ... IDLE`, which requires **Redis 6.2 or newer**. Upgrade
your Redis deployment before upgrading the library.

## 2. `metrics.Recorder` has new methods (breaking for custom recorders)

If you implement `metrics.Recorder` yourself, add the following methods (no-op bodies are fine to
start):

```go
func (r *MyRecorder) RecordReconciliationScan(requeued, skippedAlive, dlqRouted int, d time.Duration) {}
func (r *MyRecorder) RecordReQueue(streamName string, success bool) {}
func (r *MyRecorder) RecordDLQRouting(streamName string) {}
func (r *MyRecorder) RecordMutexAliveSkip(streamName string) {}
func (r *MyRecorder) RecordAckAddGap(streamName string) {}
func (r *MyRecorder) RecordTopologyReset(success bool) {}
```

`RecordClaimAttempt` is no longer emitted (kept on the interface for compatibility). Replace any
dashboards/alerts based on it with `RecordReQueue`. See `docs/METRICS.md` for the full list.

## 3. `Claim` semantics changed (signature unchanged)

`Claim(ctx, lbsInfo)` previously took direct ownership of an expired stream via `XCLAIM`. It now
acknowledges the dead consumer's pending message and re-adds it as a new message for normal
redistribution. Practical implications:

- You can keep calling `Claim` in response to a `StreamExpired` notification — no code change
  required. The consumer that ultimately picks up the re-queued work receives a `StreamAdded`
  notification (it may or may not be the one that called `Claim`).
- `Claim` returns `errs.ErrAlreadyClaimed` when another consumer already recovered the message
  (XACK-first dedup), which you can safely ignore.
- If you previously assumed the calling consumer immediately owned the stream after `Claim`
  returned, rely on the `StreamAdded` notification instead.

You may also rely solely on the periodic reconciliation scan and stop handling `StreamExpired` /
calling `Claim` altogether — recovery still happens. This is required in multi-shard clusters, where
keyspace notifications are not broadcast across shards.

## 4. New recovery configuration (optional)

Defaults preserve sensible behavior; tune via `impl.WithRecoveryConfig`:

```go
impl.WithRecoveryConfig(impl.RecoveryConfig{
    ReconciliationInterval: 60 * time.Second, // base scan period (jitter added)
    MinIdleTime:            30 * time.Second, // min idle before a pending msg is recoverable
    BatchSize:              50,               // max pending msgs inspected per scan
    MaxRetries:             3,                // re-queues before DLQ routing
    DLQStream:              "my-service-dlq", // empty => drop after MaxRetries
})
```

`impl.WithLBSIdleTime` and `impl.WithLBSRecoveryCount` no longer affect recovery and are deprecated.

## 5. Enabling OSS Redis Cluster mode

```go
clusterClient := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{...}})

client, err := impl.NewRedisStreamClient(
    clusterClient, "my-service",
    impl.WithClusterMode(impl.ClusterModeOSS),
)
```

Requirements and notes:

- The underlying client **must** be a `*redis.ClusterClient`; otherwise `Init` returns
  `errs.ErrClusterClientRequired`.
- The client enables keyspace notifications and subscribes on every master node.
- After a failover or resharding, call `client.ResetTopology(ctx)` to reload the cluster topology
  and rebuild keyspace subscriptions.
- The default `ClusterModeSingleShard` is unchanged and remains correct for single-node,
  primary/replica, and Sentinel deployments.

## 6. `RedisStreamClient` interface gained `ResetTopology`

If you implement the `types.RedisStreamClient` interface yourself (e.g. for mocks), add:

```go
func (m *MyClient) ResetTopology(ctx context.Context) error { return nil }
```

## 7. Message format note

Re-queued messages carry an extra `_retry_count` stream field (and DLQ entries additionally carry
`_dlq_reason`). Your existing `lbs-input` payload is preserved unchanged. If you inspect raw LBS
entries directly, be aware of these additional fields.
