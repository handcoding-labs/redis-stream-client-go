# Basic Usage Example

This example demonstrates the fundamental usage of the Redis Stream Client Go library.

## What This Example Shows

- How to create and initialize a Redis Stream Client
- How to handle different types of notifications (StreamAdded, StreamExpired, StreamDisowned, StreamTerminated)
- Basic stream processing workflow
- Graceful shutdown handling

## Prerequisites

1. Redis server running on `localhost:6379`
2. Go 1.22 or later
3. Environment variable set: `export POD_NAME=basic-example-consumer`

## Running the Example

1. **Start Redis server:**
   ```bash
   # Using Docker
   docker run -d -p 6379:6379 redis:7.2.3
   
   # Or use your local Redis installation
   redis-server
   ```

2. **Set environment variable:**
   ```bash
   export POD_NAME=basic-example-consumer-$(date +%s)
   ```

3. **Navigate to the example directory:**
   ```bash
   cd examples/basic-usage
   ```

4. **Run the example:**
   ```bash
   go run main.go
   ```

## Expected Output

```
2024/01/15 10:30:00 INFO Connected to Redis successfully
2024/01/15 10:30:00 INFO Created client client_id=redis-consumer-basic-example-consumer-1705312200
2024/01/15 10:30:00 INFO Client initialized successfully
2024/01/15 10:30:02 INFO Adding test data to LBS...
2024/01/15 10:30:02 INFO Added test message to LBS message_id=0 stream_id=1705312202000-0
2024/01/15 10:30:02 INFO 🎉 New stream added payload={"DataStreamName":"user-session-0","Info":{"created_at":"2024-01-15T10:30:02Z","priority":"normal","session_id":"session-0-1705312202","user_id":"user-0"}}
2024/01/15 10:30:02 INFO Processing stream stream_name=user-session-0 stream_info=map[created_at:2024-01-15T10:30:02Z priority:normal session_id:session-0-1705312202 user_id:user-0]
2024/01/15 10:30:02 INFO ✅ Finished processing stream stream_name=user-session-0
...
```

## Code Walkthrough

### 1. Client Setup

```go
// Create Redis client
redisClient := redis.NewUniversalClient(&redis.UniversalOptions{
    Addrs: []string{"localhost:6379"},
    DB:    0,
})

// Enable keyspace notifications (required for failure detection)
redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex")

// Create Redis Stream Client
client, err := impl.NewRedisStreamClient(redisClient, "basic-example")
if err != nil {
    // handle error
}
```

### 2. Initialization

```go
// Initialize the client and get the notification channel
outputChan, err := client.Init(ctx)
if err != nil {
    slog.Error("Failed to initialize client", "error", err)
    os.Exit(1)
}
```

### 3. Processing Notifications

The library uses an internal `NotificationBroker` to safely deliver notifications from multiple sources (LBS reader, keyspace listener, key extenders) to a single output channel.

```go
for notification := range outputChan {
    switch notification.Type {
    case notifs.StreamAdded:
        // Handle new stream assignment
        // notification.Payload contains LBSInfo with DataStreamName and IDInLBS
        // notification.AdditionalInfo contains the Info field from LBSInputMessage
        handleStreamAdded(ctx, notification)
        
    case notifs.StreamExpired:
        // Claim stream from failed consumer
        client.Claim(ctx, notification.Payload)
        
    case notifs.StreamDisowned:
        // Handle losing stream ownership
        handleStreamDisowned(notification)
    
    case notifs.StreamTerminated:
        // Channel is closing - check reason in notification.AdditionalInfo["info"]
        slog.Info("Notification channel closing", "reason", notification.AdditionalInfo["info"])
    }
}
```

### 4. Graceful Shutdown

```go
// Handle shutdown signals
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

// Wait for signal
<-sigChan

// Clean up - this ensures all pending notifications are drained
client.Done()
```

## Key Concepts

### Load Balancer Stream (LBS)
- The library uses a special Redis stream called the "Load Balancer Stream"
- Stream names are distributed among consumers in round-robin fashion
- Each consumer gets assigned different data streams to process
- Messages are added to LBS using the `LBSInputMessage` structure with `DataStreamName` and `Info` fields
- The `Info` field allows passing additional metadata that becomes available in `AdditionalInfo`

### NotificationBroker (Internal)
- Multiple goroutines produce notifications: LBS reader, keyspace listener, and key extenders
- The `NotificationBroker` provides thread-safe delivery to the output channel
- Prevents panics when sending to a closed channel during shutdown
- Ensures graceful draining of pending notifications before channel closure

### Notifications
- **StreamAdded**: A new data stream has been assigned to this consumer
- **StreamExpired**: Another consumer failed, and their stream is available to claim
- **StreamDisowned**: This consumer lost ownership of a stream (usually due to network issues)
- **StreamTerminated**: The notification channel is closing (context cancelled or fatal error)

### Consumer ID
- Each consumer needs a unique ID (from POD_NAME or POD_IP environment variables)
- This ID is used for distributed locking and stream ownership

## Next Steps

After understanding this basic example, check out:
- [Load Balancing Example](../load-balancing/) - Multiple consumers working together
- [Failure Recovery Example](../failure-recovery/) - Handling consumer failures

## Troubleshooting

### "POD_NAME or POD_IP not set"
Set the environment variable:
```bash
export POD_NAME=my-consumer-$(date +%s)
```

### No messages received
1. Check if Redis is running: `redis-cli ping`
2. Verify keyspace notifications: `redis-cli CONFIG GET notify-keyspace-events`
3. Check Redis logs for any errors

### Connection refused
Ensure Redis is running on the correct port:
```bash
redis-server --port 6379
```

### Output channel closed unexpectedly
Check for `StreamTerminated` notifications - they contain the reason for closure. Common causes:
- Context cancellation
- Redis connection errors
- Fatal processing errors
