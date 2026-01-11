# Operations Guide

## Memory Implications

### Buffered Channels

| Channel | Default Size | Memory per Item | Purpose |
|---------|--------------|-----------------|---------|
| Output channel | 500 | ~200 bytes | Notifications to consumer |
| Broker input | 500 | ~200 bytes | Internal broker queue |
| Keyspace channel | 100 | ~100 bytes | Redis pub/sub messages |

**Base memory per client:** ~250KB for channels alone

### Per-Stream Memory

Each active stream consumes:
- **Goroutine stack:** ~2KB (grows as needed)
- **Mutex state:** ~200 bytes (redsync mutex)
- **StreamLocksInfo:** ~150 bytes (LBSInfo + metadata)

**Formula:** `Total = Base(250KB) + Streams × 2.5KB`

| Active Streams | Approximate Memory |
|----------------|-------------------|
| 10 | ~275 KB |
| 100 | ~500 KB |
| 1,000 | ~2.75 MB |
| 10,000 | ~25 MB |

### Redis Memory

| Key Type | Count | Memory |
|----------|-------|--------|
| Mutex keys | 1 per active stream | ~100 bytes each |
| LBS stream | 1 per service | Grows until acknowledged |
| Consumer group | 1 per service | Small, fixed |

## Operational Limits

### Recommended Limits

| Metric | Recommended | Hard Limit | Notes |
|--------|-------------|------------|-------|
| Concurrent streams per client | < 1,000 | ~10,000 | Beyond 1K, monitor Redis load |
| Clients per LBS | < 50 | ~100 | Consumer group overhead |
| Total concurrent streams (cluster) | < 10,000 | ~50,000 | Depends on Redis capacity |
| Stream hold time | < 30 min | Hours OK | Longer = more memory |
| hbInterval | 10-60s | > 5s | Lower = more Redis ops |

### What Breaks First

**At 1,000+ streams per client:**
- Redis EXPIRE commands scale linearly (one per stream per `hbInterval/2`)
- 1,000 streams × 10s interval = 200 EXPIRE/sec per client
- Usually fine, but monitor if Redis is shared

**At 5,000+ streams per client:**
- Timer scheduling overhead noticeable
- ~10MB memory for goroutine stacks
- Redis command rate: 1,000+ ops/sec just for lock extension
- Consider if this is the right architecture

**At 10,000+ streams per client:**
- Current per-stream goroutine model becomes inefficient
- ~20MB+ memory, 2,000+ Redis ops/sec
- Batch lock extension would be needed (not currently implemented)

### Scaling Patterns

**Horizontal scaling (recommended):**
```
10 pods × 100 streams/pod = 1,000 total streams ✓
20 pods × 100 streams/pod = 2,000 total streams ✓
```

**Vertical scaling (less ideal):**
```
1 pod × 1,000 streams = approaching limit ⚠️
1 pod × 5,000 streams = monitor closely ✗
```

### Pod Failure Impact

When a pod fails, its streams expire after `hbInterval` and redistribute:

| Cluster Size | Pods Failed | Streams Redistributed | New Load/Pod |
|--------------|-------------|----------------------|--------------|
| 10 pods × 100 | 1 | 100 | ~111 |
| 10 pods × 100 | 2 | 200 | ~125 |
| 10 pods × 100 | 5 | 500 | ~200 |

**Design for 2× normal load** to handle failure scenarios.

### When to Consider Alternatives

This library is optimized for:
- Hundreds to low thousands of concurrent streams per client
- Stream hold times of seconds to minutes
- Distributed processing across multiple pods

Consider alternatives if you need:
- 10,000+ concurrent streams per client → batch lock extension (not implemented)
- Sub-second stream handoff → different coordination mechanism
- Millions of streams → dedicated job queue (SQS, Kafka)

## Backpressure & Slow Consumers

### What Happens

1. **Output channel fills up** (default: 500 notifications)
2. **Broker blocks** waiting for space
3. **Upstream backs up** - LBS reader, keyspace listener, key extenders block
4. **Redis consumer lag increases** - messages accumulate in PEL

### Symptoms

- Growing `XPENDING` count in Redis
- Increased Redis memory usage
- Delayed notifications
- Streams getting reclaimed by other consumers

### Mitigation

```go
// 1. Process notifications concurrently
for notification := range outputChan {
    go handleNotification(notification)
}

// 2. Use worker pool for controlled concurrency
workerPool := make(chan struct{}, 10)
for notification := range outputChan {
    workerPool <- struct{}{}
    go func(n notifs.RecoverableRedisNotification) {
        defer func() { <-workerPool }()
        handleNotification(n)
    }(notification)
}

// 3. Call DoneStream() promptly
client.DoneStream(ctx, streamName)
```

## Monitoring

### Redis Commands

```bash
# Check pending messages per consumer
redis-cli XPENDING my-service-input my-service-group

# Check consumer lag
redis-cli XINFO CONSUMERS my-service-input my-service-group

# Check LBS stream length
redis-cli XLEN my-service-input

# Check memory usage
redis-cli INFO memory

# Check keyspace notifications enabled
redis-cli CONFIG GET notify-keyspace-events
```

### Alert Thresholds

| Metric | Warning | Critical |
|--------|---------|----------|
| XPENDING count | > 2× baseline | > 10× baseline |
| Consumer lag | > 30s | > 5min |
| Goroutines per pod | > 1,500 | > 5,000 |
| Stream hold time | > 30min | > 2hr |

### Application Metrics to Export

```go
// Goroutine count
runtime.NumGoroutine()

// Active stream count (implement in your code)
activeStreams.Load()

// Processing latency histogram
processingDuration.Observe(elapsed.Seconds())
```

## Troubleshooting

### "POD_NAME or POD_IP not set"

Set one of the required environment variables:

```bash
export POD_NAME=my-consumer-$(date +%s)
# OR
export POD_IP=$(hostname -I | awk '{print $1}')
```

### No Notifications Received

**Check keyspace notifications:**
```bash
redis-cli CONFIG GET notify-keyspace-events
# Should include "Ex"
```

**Check LBS stream exists:**
```bash
redis-cli XINFO STREAM my-service-input
```

**Check consumer group:**
```bash
redis-cli XINFO GROUPS my-service-input
```

### "Failed to claim expired stream" Errors

**Expected behavior.** Multiple consumers race to claim expired streams. Only one wins.

Handle gracefully:
```go
if err := client.Claim(ctx, notification.Payload); err != nil {
    log.Debug("Another consumer claimed it first")
}
```

### High Memory Usage

**Check streams not being acknowledged:**
```bash
redis-cli XPENDING my-service-input my-service-group
```

**Ensure `DoneStream()` called:**
```go
defer client.DoneStream(ctx, streamName)
```

**Check stream length (trim if needed):**
```bash
redis-cli XLEN my-service-input
redis-cli XTRIM my-service-input MAXLEN ~ 10000
```

### Consumer Appears Stuck

**Possible causes:**
1. Network issues to Redis
2. Processing taking longer than idle time
3. Deadlock in processing logic

**Solutions:**
- Implement timeouts in processing
- Use context cancellation
- Increase `WithLBSIdleTime` if processing legitimately slow

### Output Channel Closed Unexpectedly

Check `StreamTerminated` notification for reason:

```go
case notifs.StreamTerminated:
    log.Error("Channel closed", "reason", notification.AdditionalInfo["info"])
```

Common causes:
- Context cancellation
- Redis connection error
- Explicit `Done()` call

## Memory Management Best Practices

```go
// 1. Always call DoneStream()
defer client.DoneStream(ctx, streamName)

// 2. Implement circuit breaker
const maxStreams = 1000
if activeCount > maxStreams {
    log.Warn("Too many streams, rejecting")
    return
}

// 3. Tune channel sizes if memory constrained
client, _ := impl.NewRedisStreamClient(
    redisClient,
    "my-service",
    impl.WithKspChanSize(50),
)

// 4. Monitor and alert on stream count
go func() {
    for range time.Tick(time.Minute) {
        if activeStreams.Load() > 800 {
            alerting.Warn("Approaching stream limit")
        }
    }
}()
```
