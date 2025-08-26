# Redis Stream Client Go - Examples

This directory contains practical examples demonstrating how to use the Redis Stream Client Go library in various scenarios. These are demo only.

## Examples Overview

### 1. [Basic Usage](./basic-usage/)
Simple example showing how to set up a client, initialize it, and handle basic stream operations.

### 2. [Load Balancing](./load-balancing/)
Demonstrates how multiple consumers work together to process streams in a load-balanced fashion.

## Prerequisites

Before running the examples, ensure you have:

1. **Go 1.22 or later** installed
2. **Redis server** running (version 6.0+)
3. **Environment variables** set:
   ```bash
   export POD_NAME=example-consumer-1
   # OR
   export POD_IP=127.0.0.1
   ```

## Running Examples

Each example directory contains:
- `main.go` - The main example code
- `README.md` - Specific instructions for that example
- `go.mod` - Go module file (if needed)

### Quick Start

1. **Start Redis server:**
   ```bash
   # Using Docker
   docker run -d -p 6379:6379 redis:7.2.3
   
   # Or using local installation
   redis-server
   ```

2. **Navigate to an example:**
   ```bash
   cd examples/basic-usage
   ```

3. **Set environment variables:**
   ```bash
   export POD_NAME=example-consumer-$(date +%s)
   ```

4. **Run the example:**
   ```bash
   go run main.go
   ```

## Common Configuration

All examples use similar Redis configuration:

```go
// Redis client setup
redisClient := redis.NewUniversalClient(&redis.UniversalOptions{
    Addrs: []string{"localhost:6379"},
    DB:    0,
})

// Enable keyspace notifications for expired events
redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex")

// Create Redis Stream Client
client := impl.NewRedisStreamClient(redisClient, "my-service")
```

## Understanding the Flow

### 1. **Initialization**
```go
outputChan, err := client.Init(ctx)
if err != nil {
    log.Fatal(err)
}
```

### 2. **Message Processing**
```go
for notification := range outputChan {
    switch notification.Type {
    case notifs.StreamAdded:
        // New stream assigned to this consumer
        handleNewStream(notification.Payload)
    case notifs.StreamExpired:
        // Another consumer failed, claim their stream
        client.Claim(ctx, notification.Payload.(string))
    case notifs.StreamDisowned:
        // This consumer lost ownership
        handleStreamLoss()
    }
}
```

### 3. **Cleanup**
```go
defer client.Done()
```

## Testing with Multiple Consumers

To test load balancing and failure recovery:

1. **Terminal 1:**
   ```bash
   export POD_NAME=consumer-1
   cd examples/load-balancing
   go run main.go
   ```

2. **Terminal 2:**
   ```bash
   export POD_NAME=consumer-2
   cd examples/load-balancing
   go run main.go
   ```

3. **Terminal 3 (Producer):**
   ```bash
   cd examples/load-balancing
   go run producer.go
   ```

## Troubleshooting

### Common Issues

1. **"POD_NAME or POD_IP not set"**
   - Set one of the required environment variables
   - These are used to create unique consumer IDs

2. **Redis connection errors**
   - Ensure Redis server is running on localhost:6379
   - Check Redis server logs for any issues

3. **No messages received**
   - Verify keyspace notifications are enabled: `CONFIG GET notify-keyspace-events`
   - Should return `Ex` or similar pattern

4. **Tests failing**
   - Ensure no other Redis clients are using the same consumer group
   - Clear Redis data: `FLUSHALL`

### Debug Mode

Enable debug logging in examples:

```go
import "log"

// Add at the beginning of main()
log.SetFlags(log.LstdFlags | log.Lshortfile)
```

## Contributing

When adding new examples:

1. Create a new directory under `examples/`
2. Include a `README.md` with specific instructions
3. Add the example to this main README
4. Ensure the example is self-contained and well-documented
5. Test the example thoroughly

## Support

For questions about the examples:
- Check the main [README](../README.md)
- Review [CONTRIBUTING.md](../CONTRIBUTING.md)
- Open an issue on GitHub
