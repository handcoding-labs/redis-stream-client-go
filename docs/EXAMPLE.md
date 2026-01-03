# Complete Example

A full working example demonstrating client initialization, notification handling, stream processing, and graceful shutdown.

## Code

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
        return
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
                slog.Info("New stream assigned", 
                    "stream", notification.Payload.DataStreamName)
                go processStream(ctx, client, notification)

            case notifs.StreamExpired:
                if err := client.Claim(ctx, notification.Payload); err != nil {
                    slog.Warn("Failed to claim stream", "error", err)
                } else {
                    slog.Info("Claimed expired stream", 
                        "stream", notification.Payload.DataStreamName)
                    go processStream(ctx, client, notification)
                }

            case notifs.StreamDisowned:
                slog.Warn("Lost stream ownership", 
                    "stream", notification.Payload.DataStreamName)

            case notifs.StreamTerminated:
                slog.Info("Notification channel closing", 
                    "reason", notification.AdditionalInfo["info"])
            }
        }
    }()

    // Add test message to LBS
    go addTestMessage(ctx, redisClient)

    // Wait for shutdown signal
    <-sigChan
    slog.Info("Shutting down...")
    client.Done()
    slog.Info("Shutdown complete")
}

func processStream(
    ctx context.Context, 
    client types.RedisStreamClient, 
    notification notifs.RecoverableRedisNotification,
) {
    streamName := notification.Payload.DataStreamName
    slog.Info("Processing stream", 
        "stream", streamName, 
        "info", notification.AdditionalInfo)

    // Simulate processing work
    time.Sleep(2 * time.Second)

    // Mark stream as done - releases lock and acknowledges LBS message
    if err := client.DoneStream(ctx, streamName); err != nil {
        slog.Error("Failed to mark stream done", 
            "error", err, 
            "stream", streamName)
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
    
    slog.Info("Added test message to LBS")
}
```

## Running the Example

### Prerequisites

1. Redis running locally:
```bash
docker run -d --name redis -p 6379:6379 redis:7
```

2. Enable keyspace notifications:
```bash
redis-cli CONFIG SET notify-keyspace-events Ex
```

### Run

```bash
go run main.go
```

### Expected Output

```
INFO New stream assigned stream=test-stream-1
INFO Processing stream stream=test-stream-1 info=map[priority:high user_id:user-123]
INFO Stream processing completed stream=test-stream-1
^C
INFO Shutting down...
INFO Shutdown complete
```

## Production Patterns

### Worker Pool

For controlled concurrency:

```go
const maxWorkers = 10
workerPool := make(chan struct{}, maxWorkers)

for notification := range outputChan {
    switch notification.Type {
    case notifs.StreamAdded, notifs.StreamExpired:
        workerPool <- struct{}{} // Acquire worker slot
        go func(n notifs.RecoverableRedisNotification) {
            defer func() { <-workerPool }() // Release slot
            
            if n.Type == notifs.StreamExpired {
                if err := client.Claim(ctx, n.Payload); err != nil {
                    return
                }
            }
            processStream(ctx, client, n)
        }(notification)
    }
}
```

### With Timeout

Prevent stuck processing:

```go
func processStreamWithTimeout(
    ctx context.Context,
    client types.RedisStreamClient,
    notification notifs.RecoverableRedisNotification,
    timeout time.Duration,
) {
    streamName := notification.Payload.DataStreamName
    
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    
    done := make(chan struct{})
    go func() {
        defer close(done)
        // Actual processing work
        doWork(ctx, streamName)
    }()
    
    select {
    case <-done:
        client.DoneStream(ctx, streamName)
    case <-ctx.Done():
        slog.Warn("Processing timeout", "stream", streamName)
        // Lock will expire, another consumer will claim
    }
}
```

### Multiple Services

Running multiple independent services:

```go
// Service A - handles user sessions
sessionClient, _ := impl.NewRedisStreamClient(redisClient, "sessions")

// Service B - handles payments  
paymentClient, _ := impl.NewRedisStreamClient(redisClient, "payments")

// Each has its own LBS: sessions-input, payments-input
```

### Health Check Endpoint

```go
func healthHandler(w http.ResponseWriter, r *http.Request) {
    status := map[string]interface{}{
        "healthy":       true,
        "active_streams": activeStreamCount.Load(),
        "goroutines":    runtime.NumGoroutine(),
    }
    json.NewEncoder(w).Encode(status)
}
```
