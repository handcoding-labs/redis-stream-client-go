package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/impl"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
)

func main() {
	// Set up context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create Redis client
	// Use environment variable for Redis address, default to localhost for local development
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisClient := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs: []string{redisAddr},
		DB:    0,
	})
	defer redisClient.Close()

	// Test Redis connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("Connected to Redis successfully")

	// Enable keyspace notifications for expired events
	if err := redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
		slog.Error("Failed to enable keyspace notifications", "error", err)
		os.Exit(1)
	}

	// Create Redis Stream Client
	client, err := impl.NewRedisStreamClient(redisClient, "basic-example")
	if err != nil {
		slog.Error("could not initialize", "error", err.Error())
	}
	slog.Info("Created client", "client_id", client.ID())

	// Initialize the client
	outputChan, err := client.Init(ctx)
	if err != nil {
		slog.Error("Failed to initialize client", "error", err)
		os.Exit(1)
	}
	slog.Info("Client initialized successfully")

	// Start a goroutine to add some test data
	go addTestData(ctx, redisClient)

	// Process notifications
	go func() {
		for notification := range outputChan {
			switch notification.Type {
			case notifs.StreamAdded:
				slog.Info("🎉 New stream added", "payload", notification.Payload)
				handleStreamAdded(ctx, notification)

			case notifs.StreamExpired:
				slog.Warn("⚠️  Stream expired", "payload", notification.Payload)
				if err := client.Claim(ctx, notification.Payload); err != nil {
					slog.Error("Failed to claim expired stream", "error", err)
				} else {
					slog.Info("✅ Successfully claimed expired stream")
				}

			case notifs.StreamDisowned:
				slog.Warn("❌ Stream disowned", "payload", notification.Payload)
				// Handle losing ownership of a stream
				handleStreamDisowned(notification)

			default:
				slog.Warn("Unknown notification type", "type", notification.Type)
			}
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	slog.Info("Shutdown signal received, cleaning up...")

	// Graceful shutdown
	if err := client.Done(ctx); err != nil {
		slog.Error("Error during cleanup", "error", err)
	} else {
		slog.Info("Client cleanup completed successfully")
	}

	slog.Info("Example completed")
}

// addTestData adds some test streams to the Load Balancer Stream (LBS)
func addTestData(ctx context.Context, redisClient redis.UniversalClient) {
	// Wait a bit for the client to be ready
	time.Sleep(2 * time.Second)

	slog.Info("Adding test data to LBS...")

	for i := 0; i < 3; i++ {
		// Create a test message for the LBS
		lbsMessage := notifs.LBSInputMessage{
			DataStreamName: fmt.Sprintf("user-session-%d", i),
			Info: map[string]interface{}{
				"user_id":    fmt.Sprintf("user-%d", i),
				"session_id": fmt.Sprintf("session-%d-%d", i, time.Now().Unix()),
				"created_at": time.Now().Format(time.RFC3339),
				"priority":   "normal",
			},
		}

		// Marshal to JSON
		messageData, err := json.Marshal(lbsMessage)
		if err != nil {
			slog.Error("Failed to marshal LBS message", "error", err)
			continue
		}

		// Add to the LBS stream
		result := redisClient.XAdd(ctx, &redis.XAddArgs{
			Stream: "basic-example-input", // LBS stream name
			Values: map[string]interface{}{
				"lbs-input": string(messageData),
			},
		})

		if result.Err() != nil {
			slog.Error("Failed to add message to LBS", "error", result.Err())
		} else {
			slog.Info("Added test message to LBS", "message_id", i, "stream_id", result.Val())
		}

		// Wait between messages
		time.Sleep(1 * time.Second)
	}
}

// handleStreamAdded processes a newly assigned stream
func handleStreamAdded(_ context.Context, lbsInfo notifs.RecoverableRedisNotification) {
	slog.Info("Processing stream", "stream_name", lbsInfo.Payload.DataStreamName, "stream_info", lbsInfo.AdditionalInfo)

	// Simulate processing the data stream
	// In a real application, you would:
	// 1. Read from the actual data stream using XREAD or XREADGROUP
	// 2. Process the messages
	// 3. Acknowledge processed messages
	// 4. Call client.DoneStream() when finished with this stream

	// Simulate some processing time
	time.Sleep(1 * time.Second)

	slog.Info("✅ Finished processing stream", "stream_name", lbsInfo.Payload.DataStreamName)

	// IMPORTANT: In a real application, you must call DoneStream when finished
	// For this demo, we're not calling it to keep streams active for demonstration
	// Uncomment the line below in production:
	// client.DoneStream(ctx, lbsInfo.Payload.DataStreamName)
}

// handleStreamDisowned handles losing ownership of a stream
func handleStreamDisowned(notification notifs.RecoverableRedisNotification) {
	slog.Info("Lost ownership of stream, stopping processing", "payload", notification.Payload)
	// In a real application, you would:
	// 1. Stop processing the stream
	// 2. Clean up any resources
	// 3. The stream will be claimed by another consumer
}
