# Metrics Reference

This document describes each metric emitted by the library via the `metrics.Recorder` interface.  The default implementation is a no‑op; you can supply a concrete recorder (e.g. the Prometheus example under `examples/prometheus`) using `impl.WithMetricsRecorder`.

| Method | Purpose | Labels / Notes |
|--------|---------|----------------|
| `RecordStartupRecovery(success bool, unackedCount int, duration time.Duration)` | Recorded once on the first (startup) reconciliation pass, which replaced the old `XAUTOCLAIM` startup recovery. | `success` = `true`/`false`. `unackedCount` reflects messages recovered (re-queued + DLQ-routed) on that pass.
| `RecordClaimAttempt(streamName string, success bool, duration time.Duration)` | _Deprecated._ `Claim` now recovers via `XACK` + `XADD`; see `RecordReQueue`. Retained on the interface for backward compatibility and no longer emitted by the library. | Labels: `stream`, `success`.
| `RecordLockAcquisitionAttempt(streamName string, success bool, duration time.Duration)` | Internal lock acquisition (including recovery on startup and when taking possession after a claim). | Labels: `stream`, `success`.
| `RecordLockExtensionAttempt(streamName string, success bool)` | A heartbeat attempt to extend an existing lock. | Labels: `stream`, `success`.
| `RecordLockReleaseAttempt(streamName string, success bool)` | Unlocking a stream (typically in `DoneStream` or when a lock expires). | Labels: `stream`, `success`.
| `RecordStreamProcessingStart(streamName string, startTime time.Time)` | Called when a stream is assigned and processing begins. | Used in combination with `RecordStreamProcessingEnd` to compute duration.
| `RecordStreamProcessingEnd(streamName string, endTime time.Time)` | Called when processing for a stream finishes. | Duration is `endTime - startTime`.
| `RecordKspNotification(streamName string)` | A keyspace notification (expiration) was received for a stream. | Label: `stream`.
| `RecordKspNotificationDropped()` | A keyspace notification was dropped due to a full internal channel. | No labels.
| `RecordReconciliationScan(requeued, skippedAlive, dlqRouted int, duration time.Duration)` | One periodic reconciliation scan completed. | `requeued` = messages re-added; `skippedAlive` = messages left in place because the lock was still held; `dlqRouted` = messages routed to the DLQ.
| `RecordReQueue(streamName string, success bool)` | A pending message was re-queued (`XACK` + `XADD`). | Labels: `stream`, `success`. `success=false` includes lost XACK races and the XACK/XADD atomicity gap.
| `RecordDLQRouting(streamName string)` | A message exceeded `MaxRetries` and was routed to the DLQ. | Label: `stream`.
| `RecordMutexAliveSkip(streamName string)` | Recovery skipped a pending message because its lock key still exists (owner alive but slow). | Label: `stream`. A high rate indicates slow consumers.
| `RecordAckAddGap(streamName string)` | `XACK` succeeded but the subsequent `XADD` failed, dropping the message from the PEL without re-queueing it. | Label: `stream`. Should be rare; non-zero values warrant investigation.
| `RecordTopologyReset(success bool)` | `ResetTopology` reloaded the cluster view and rebuilt keyspace subscriptions (`ClusterModeOSS`). | Label: `success`.

## Using with Prometheus

See `examples/prometheus/recorder.go` for a reference implementation.  To use it:

```go
import (
    prom "github.com/handcoding-labs/redis-stream-client-go/examples/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

rec := prom.NewPrometheusRecorder(prometheus.DefaultRegisterer)
client, _ := impl.NewRedisStreamClient(redisClient, "my-service", impl.WithMetricsRecorder(rec))
// …start HTTP server with promhttp.Handler() …
```

The example registers counters and histograms – feel free to copy and adapt to your own monitoring system.

## Significance

- **Startup recovery** metrics help you understand how many unprocessed streams your service recovers when it restarts.  High durations may indicate Redis latency or large pending queues.
- **Claim / lock metrics** provide visibility into contention between consumers and lock collisions.
- **Stream processing durations** quantify end-to-end time per stream (useful for SLA monitoring).
- **Keyspace notification counts** show the volume of consumer expirations, which may correlate with crash rates or slow consumers.
- A non‑zero `KspNotificationDropped` indicates backpressure in the library; consider increasing `WithKspChanSize` or processing notifications faster.

"```

