package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/impl"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types"
)

func main() {
	// Check if this is running as a producer
	if len(os.Args) > 1 && os.Args[1] == "producer" {
		runProducer()
		return
	}

	// Run as consumer
	runConsumer()
}

func runConsumer() {
	// Set up context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Get consumer ID from environment
	consumerID := os.Getenv("POD_NAME")
	if consumerID == "" {
		consumerID = fmt.Sprintf("consumer-%d", time.Now().Unix())
		os.Setenv("POD_NAME", consumerID)
	}

	slog.Info("Starting consumer", "consumer_id", consumerID)

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

	// Enable keyspace notifications
	if err := redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
		slog.Error("Failed to enable keyspace notifications", "error", err)
		os.Exit(1)
	}

	// Create Redis Stream Client
	client, err := impl.NewRedisStreamClient(
		redisClient,
		"load-balance-demo",
		impl.WithMaxRetries(-1),
		impl.WithForceConfigOverride(),
	)
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

	// Track processed streams
	var processedStreams sync.Map
	var streamCount int32

	// Process notifications
	// The internal NotificationBroker ensures thread-safe delivery from multiple sources:
	// - LBS stream reader
	// - Keyspace notification listener
	// - Key extenders (one per active stream)
	go func() {
		for notification := range outputChan {
			switch notification.Type {
			case notifs.StreamAdded:
				slog.Info("🎉 New stream assigned", "consumer_id", consumerID)
				go handleStreamAdded(ctx, client, notification, &processedStreams, &streamCount)

			case notifs.StreamExpired:
				slog.Warn("⚠️  Stream expired, attempting to claim", "consumer_id", consumerID, "payload", notification.Payload)
				if err := client.Claim(ctx, notification.Payload); err != nil {
					slog.Error("❌ Failed to claim expired stream", "consumer_id", consumerID, "error", err)
				} else {
					slog.Info("✅ Successfully claimed expired stream", "consumer_id", consumerID)
					go handleClaimedStream(ctx, client, notification.Payload.DataStreamName, &processedStreams, &streamCount)
				}

			case notifs.StreamDisowned:
				slog.Warn("❌ Stream disowned", "consumer_id", consumerID, "payload", notification.Payload)

			case notifs.StreamTerminated:
				// This notification indicates the channel is closing
				// The reason is available in AdditionalInfo
				reason := "unknown"
				if info, ok := notification.AdditionalInfo["info"].(string); ok {
					reason = info
				}
				slog.Info("📴 Notification channel terminating", "consumer_id", consumerID, "reason", reason)
			}
		}
		slog.Info("Notification channel closed", "consumer_id", consumerID)
	}()

	// Print statistics periodically
	go printStatistics(ctx, consumerID, &processedStreams, &streamCount)

	// Wait for shutdown signal
	<-sigChan
	slog.Info("🛑 Shutdown signal received, cleaning up...", "consumer_id", consumerID)

	// Graceful shutdown
	// Done() ensures all pending notifications are drained via the NotificationBroker
	// before the output channel is closed
	if err := client.Done(ctx); err != nil {
		slog.Error("❌ Error during cleanup", "consumer_id", consumerID, "error", err)
	} else {
		slog.Info("✅ Client cleanup completed", "consumer_id", consumerID)
	}
}

func runProducer() {
	ctx := context.Background()

	slog.Info("🏭 Starting producer...")

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

	// Test connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}

	// Produce messages continuously
	messageID := 0
	for {
		// Create batch of messages
		for i := 0; i < 5; i++ {
			lbsMessage := notifs.LBSInputMessage{
				DataStreamName: fmt.Sprintf("order-stream-%d", messageID),
				Info: map[string]interface{}{
					"order_id":    fmt.Sprintf("order-%d", messageID),
					"customer_id": fmt.Sprintf("customer-%d", messageID%100),
					"amount":      float64(messageID%1000 + 100),
					"created_at":  time.Now().Format(time.RFC3339),
					"status":      "pending",
					"priority":    []string{"low", "normal", "high"}[messageID%3],
				},
			}

			messageData, err := json.Marshal(lbsMessage)
			if err != nil {
				slog.Error("❌ Failed to marshal message", "error", err)
				continue
			}

			result := redisClient.XAdd(ctx, &redis.XAddArgs{
				Stream: "load-balance-demo-input",
				Values: map[string]interface{}{
					"lbs-input": string(messageData),
				},
			})

			if result.Err() != nil {
				slog.Error("❌ Failed to add message", "error", result.Err())
			} else {
				slog.Info("📤 Produced message", "message_id", messageID, "stream_name", lbsMessage.DataStreamName)
			}

			messageID++
		}

		// Wait before next batch
		time.Sleep(3 * time.Second)
	}
}

func handleStreamAdded(ctx context.Context, client types.RedisStreamClient, notification notifs.RecoverableRedisNotification, processedStreams *sync.Map, streamCount *int32) {
	consumerID := client.ID()
	streamName := notification.Payload.DataStreamName

	slog.Info("🔄 Processing stream", "consumer_id", consumerID, "stream_name", streamName)
	slog.Debug("📋 Stream details", "consumer_id", consumerID, "stream_info", notification.Payload)

	// Simulate processing time (varies by priority)
	processingTime := 2 * time.Second
	if priority, ok := notification.AdditionalInfo["priority"].(string); ok {
		switch priority {
		case "high":
			processingTime = 1 * time.Second
		case "low":
			processingTime = 4 * time.Second
		}
	}

	// Simulate work
	time.Sleep(processingTime)

	// Mark as processed
	processedStreams.Store(streamName, time.Now())
	*streamCount++

	// Mark stream as done after processing
	if err := client.DoneStream(ctx, streamName); err != nil {
		slog.Error("Failed to mark stream done", "error", err, "stream", streamName, "consumer_id", consumerID)
	} else {
		slog.Info("✅ Completed processing stream", "consumer_id", consumerID, "stream_name", streamName, "processing_time", processingTime)
	}
}

func handleClaimedStream(ctx context.Context, client types.RedisStreamClient, streamName string, processedStreams *sync.Map, streamCount *int32) {
	slog.Info("🔄 Processing claimed stream", "consumer_id", client.ID(), "stream_name", streamName)

	// Simulate processing the claimed stream
	time.Sleep(1 * time.Second)

	processedStreams.Store(streamName, time.Now())
	*streamCount++

	// Mark stream as done after processing
	if err := client.DoneStream(ctx, streamName); err != nil {
		slog.Error("Failed to mark claimed stream done", "error", err, "stream", streamName, "consumer_id", client.ID())
	} else {
		slog.Info("✅ Completed processing claimed stream", "consumer_id", client.ID(), "stream_name", streamName)
	}
}

func printStatistics(ctx context.Context, consumerID string, processedStreams *sync.Map, streamCount *int32) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count := *streamCount
			slog.Info("📊 Statistics", "consumer_id", consumerID, "processed_streams", count)

			// Show recent streams
			recentCount := 0
			processedStreams.Range(func(key, value interface{}) bool {
				if processedTime, ok := value.(time.Time); ok {
					if time.Since(processedTime) < 10*time.Second {
						recentCount++
					}
				}
				return true
			})

			if recentCount > 0 {
				slog.Info("📈 Recent activity", "consumer_id", consumerID, "recent_streams", recentCount)
			}
		}
	}
}
