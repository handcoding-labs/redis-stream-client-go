# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- **Redis Cluster (OSS) support** ([#60](https://github.com/handcoding-labs/redis-stream-client-go/issues/60)).
  - `impl.WithClusterMode(impl.ClusterModeSingleShard | impl.ClusterModeOSS)`. In `ClusterModeOSS`
    the client enables keyspace notifications and subscribes on every master node
    ([#108](https://github.com/handcoding-labs/redis-stream-client-go/issues/108)).
  - `RedisStreamClient.ResetTopology(ctx)` to reload the cluster view and rebuild keyspace
    subscriptions after failover/resharding
    ([#109](https://github.com/handcoding-labs/redis-stream-client-go/issues/109)).
- **Periodic reconciliation scan** that recovers pending LBS messages whose owning consumer is dead,
  configurable via `impl.WithRecoveryConfig(impl.RecoveryConfig{...})`
  ([#107](https://github.com/handcoding-labs/redis-stream-client-go/issues/107)). The scan timer is
  jittered to avoid synchronized scans across consumers
  ([#110](https://github.com/handcoding-labs/redis-stream-client-go/issues/110)).
- **`_retry_count` field and DLQ routing**: re-queued messages carry a retry count; once it exceeds
  `RecoveryConfig.MaxRetries` they are routed to `RecoveryConfig.DLQStream` (defaults to
  `<lbs>-dlq`). The DLQ is capped with an approximate `MAXLEN` trim via `RecoveryConfig.DLQMaxLen`
  (default 10000; `0` disables the cap) so it cannot grow unbounded
  ([#106](https://github.com/handcoding-labs/redis-stream-client-go/issues/106)).
- New metrics on `metrics.Recorder`: `RecordReconciliationScan`, `RecordReQueue`,
  `RecordDLQRouting`, `RecordMutexAliveSkip`, `RecordAckAddGap`, `RecordTopologyReset`
  ([#113](https://github.com/handcoding-labs/redis-stream-client-go/issues/113)).
- Integration tests for the mutex-liveness check, XACK-first dedup, multi-shard recovery, DLQ
  routing, and (infra-gated) OSS cluster keyspace subscription
  ([#111](https://github.com/handcoding-labs/redis-stream-client-go/issues/111),
  [#112](https://github.com/handcoding-labs/redis-stream-client-go/issues/112),
  [#114](https://github.com/handcoding-labs/redis-stream-client-go/issues/114),
  [#115](https://github.com/handcoding-labs/redis-stream-client-go/issues/115)).

### Changed
- **Recovery now uses `XACK` + `XADD` instead of `XCLAIM`/`XAUTOCLAIM`.** This is cluster-safe and
  fixes a duplicate-processing bug where a slow-but-alive consumer could have its message reclaimed.
  Before re-queuing, the scan verifies the consumer's lock key no longer exists.
- `Claim(ctx, lbsInfo)` now re-queues the expired task (`XACK` + `XADD`) for normal redistribution
  rather than taking direct ownership via `XCLAIM`. The signature is unchanged; it returns
  `ErrAlreadyClaimed` when another consumer already recovered the message.
- Startup recovery is now the first pass of the reconciliation scan (the dedicated `XAUTOCLAIM`
  startup recovery was removed).

### Deprecated
- `metrics.Recorder.RecordClaimAttempt` is no longer emitted (kept for interface compatibility); use
  `RecordReQueue` instead.
- `impl.WithLBSIdleTime` and `impl.WithLBSRecoveryCount` no longer affect recovery (which is now
  governed by `RecoveryConfig`); they remain for API compatibility and will be removed in a future
  release.

### Breaking changes
- `metrics.Recorder` gained new methods; custom implementations must implement them. See
  `docs/MIGRATION.md`.
- `types.RedisStreamClient` gained `ResetTopology(ctx) error`.
- Recovery requires **Redis 6.2+** (the scan uses `XPENDING ... IDLE`).

See [`docs/MIGRATION.md`](docs/MIGRATION.md) for upgrade steps.
