# Load Balancing Example

This example demonstrates how multiple consumers work together to process streams in a load-balanced fashion using the Redis Stream Client Go library.

## What This Example Shows

- Multiple consumers processing streams concurrently
- Load balancing of stream assignments across consumers
- Producer generating continuous stream of messages
- Statistics and monitoring of consumer performance
- Automatic claiming of streams from failed consumers
- Handling all notification types including StreamTerminated

## Architecture

```
Producer → LBS (Load Balancer Stream) → Consumer 1
                                     → Consumer 2
                                     → Consumer 3
                                     → ...
```

The Load Balancer Stream (LBS) distributes incoming stream assignments to available consumers in a round-robin fashion.

### Internal NotificationBroker

Each consumer uses an internal `NotificationBroker` that safely manages notifications from multiple concurrent sources:
- LBS stream reader
- Keyspace notification listener  
- Key extenders (one per active stream)

This ensures thread-safe delivery and graceful shutdown without panics.

## Prerequisites

1. Redis server running on `localhost:6379`
2. Go 1.22 or later
3. Multiple terminal windows for running multiple consumers

## Running the Example

### Step 1: Start Redis Server

```bash
# Using Docker
docker run -d -p 6379:6379 redis:7.2.3

# Or use your local Redis installation
redis-server
```

### Step 2: Start Multiple Consumers

Open multiple terminal windows and run:

**Terminal 1 (Consumer 1):**
```bash
export POD_NAME=consumer-1
cd examples/load-balancing
go run main.go
```

**Terminal 2 (Consumer 2):**
```bash
export POD_NAME=consumer-2
cd examples/load-balancing
go run main.go
```

**Terminal 3 (Consumer 3):**
```bash
export POD_NAME=consumer-3
cd examples/load-balancing
go run main.go
```

### Step 3: Start the Producer

**Terminal 4 (Producer):**
```bash
cd examples/load-balancing
go run main.go producer
```

## Expected Output

### Consumer Output:
```
2024/01/15 10:30:00 Starting consumer: consumer-1
2024/01/15 10:30:00 Created client with ID: redis-consumer-consumer-1
2024/01/15 10:30:00 🎉 [consumer-1] New stream assigned
2024/01/15 10:30:00 🔄 [consumer-1] Processing stream: order-stream-0
2024/01/15 10:30:00 📋 [consumer-1] Stream details: map[amount:100 created_at:2024-01-15T10:30:00Z customer_id:customer-0 order_id:order-0 priority:low status:pending]
2024/01/15 10:30:04 ✅ [consumer-1] Completed processing stream: order-stream-0 (took 4s)
2024/01/15 10:30:10 📊 [consumer-1] Statistics: Processed 1 streams
```

### Producer Output:
```
2024/01/15 10:30:05 🏭 Starting producer...
2024/01/15 10:30:05 📤 Produced message 0: order-stream-0
2024/01/15 10:30:05 📤 Produced message 1: order-stream-1
2024/01/15 10:30:05 📤 Produced message 2: order-stream-2
2024/01/15 10:30:05 📤 Produced message 3: order-stream-3
2024/01/15 10:30:05 📤 Produced message 4: order-stream-4
```

## Key Features Demonstrated

### 1. Load Balancing
- Streams are distributed among consumers in round-robin fashion
- Each consumer processes different streams
- No single consumer gets overwhelmed

### 2. Priority Processing
The example includes three priority levels:
- **High priority**: 1 second processing time
- **Normal priority**: 2 seconds processing time  
- **Low priority**: 4 seconds processing time

### 3. Statistics Monitoring
Each consumer reports:
- Total streams processed
- Recent activity (last 10 seconds)
- Processing times and completion status

### 4. Failure Recovery
If you kill a consumer (Ctrl+C), other consumers will automatically claim its unprocessed streams.

### 5. Graceful Shutdown
The `NotificationBroker` ensures:
- All pending notifications are processed before shutdown
- No panics occur when sending to closing channels
- Clean resource cleanup

## Testing Scenarios

### Scenario 1: Basic Load Balancing
1. Start 3 consumers
2. Start the producer
3. Observe how streams are distributed among consumers

### Scenario 2: Consumer Failure
1. Start 3 consumers and producer
2. Kill one consumer (Ctrl+C)
3. Observe other consumers claiming the failed consumer's streams

### Scenario 3: Dynamic Scaling
1. Start with 1 consumer and producer
2. Add more consumers while producer is running
3. Observe load redistribution

### Scenario 4: High Load Testing
1. Start multiple consumers
2. Modify producer to generate more messages
3. Monitor consumer statistics

## Code Walkthrough

### Producer Logic
```go
// Create messages with different priorities
lbsMessage := notifs.LBSInputMessage{
    DataStreamName: fmt.Sprintf("order-stream-%d", messageID),
    Info: map[string]interface{}{
        "order_id":    fmt.Sprintf("order-%d", messageID),
        "customer_id": fmt.Sprintf("customer-%d", messageID%100),
        "amount":      float64(messageID%1000 + 100),
        "priority":    []string{"low", "normal", "high"}[messageID%3],
        "status":      "pending",
        "created_at":  time.Now().Format(time.RFC3339),
    },
}

// Marshal to JSON
messageData, err := json.Marshal(lbsMessage)
if err != nil {
    return err
}

// Add to LBS
redisClient.XAdd(ctx, &redis.XAddArgs{
    Stream: "load-balance-demo-input",
    Values: map[string]interface{}{
        "lbs-input": string(messageData),
    },
})
```

### Consumer Processing
```go
// Handle new stream assignment
case notifs.StreamAdded:
    // notification.Payload contains LBSInfo with DataStreamName and IDInLBS
    // notification.AdditionalInfo contains the Info field from LBSInputMessage
    go handleStreamAdded(ctx, client, notification, &processedStreams, &streamCount)

// Handle claiming from failed consumer
case notifs.StreamExpired:
    client.Claim(ctx, notification.Payload)
    go handleClaimedStream(ctx, client, notification.Payload.DataStreamName, &processedStreams, &streamCount)

// Handle channel termination
case notifs.StreamTerminated:
    slog.Info("Notification channel closing", "reason", notification.AdditionalInfo["info"])
```

### Statistics Tracking
```go
// Track processed streams
var processedStreams sync.Map
var streamCount int32

// Update counters
processedStreams.Store(streamName, time.Now())
atomic.AddInt32(&streamCount, 1)
```

## Monitoring and Debugging

### Redis CLI Commands
```bash
# Check LBS stream
redis-cli XINFO STREAM load-balance-demo-input

# Check consumer group
redis-cli XINFO GROUPS load-balance-demo-input

# Check pending messages
redis-cli XPENDING load-balance-demo-input load-balance-demo-group

# Monitor keyspace notifications
redis-cli --csv psubscribe '__keyevent@0__:expired'
```

### Environment Variables
```bash
# Set unique consumer IDs
export POD_NAME=consumer-$(hostname)-$(date +%s)

# Enable debug logging
export DEBUG=1
```

## Performance Tuning

### Consumer Optimization
- Adjust processing times based on your use case
- Implement proper error handling and retries
- Use connection pooling for database operations

### Producer Optimization  
- Batch message production
- Use pipelining for better throughput
- Monitor Redis memory usage

### Redis Configuration
```bash
# Increase memory if needed
redis-cli CONFIG SET maxmemory 1gb

# Optimize for streams
redis-cli CONFIG SET stream-node-max-entries 1000
```

## Troubleshooting

### No Load Balancing
- Ensure all consumers use the same service name
- Check that consumers have unique POD_NAME values
- Verify Redis keyspace notifications are enabled

### Messages Not Processing
- Check Redis connection and authentication
- Verify LBS stream exists: `redis-cli XLEN load-balance-demo-input`
- Check for errors in consumer logs

### High Memory Usage
- Monitor stream lengths: `redis-cli XLEN <stream-name>`
- Implement proper message acknowledgment
- Consider stream trimming for old messages

### Unexpected Channel Closure
- Check for `StreamTerminated` notifications - they contain the reason
- Common causes: context cancellation, Redis connection errors
- The `NotificationBroker` ensures clean shutdown without data loss

## Next Steps

After understanding load balancing, check out:
- [Failure Recovery Example](../failure-recovery/) - Advanced failure handling
- [Basic Usage Example](../basic-usage/) - Fundamental concepts
