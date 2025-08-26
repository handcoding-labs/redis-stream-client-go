package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis successfully")

	// Enable keyspace notifications for expired events
	if err := redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
		log.Fatalf("Failed to enable keyspace notifications: %v", err)
	}

	// Create Redis Stream Client
	client := impl.NewRedisStreamClient(redisClient, "basic-example")
	log.Printf("Created client with ID: %s", client.ID())

	// Initialize the client
	outputChan, err := client.Init(ctx)
	if err != nil {
		log.Fatalf("Failed to initialize client: %v", err)
	}
	log.Println("Client initialized successfully")

	// Start a goroutine to add some test data
	go addTestData(ctx, redisClient)

	// Process notifications
	go func() {
		for notification := range outputChan {
			switch notification.Type {
			case notifs.StreamAdded:
				log.Printf("🎉 New stream added: %v", notification.Payload)
				handleStreamAdded(ctx, notification.Payload.(string))

			case notifs.StreamExpired:
				log.Printf("⚠️  Stream expired: %v", notification.Payload)
				if err := client.Claim(ctx, notification.Payload.(string)); err != nil {
					log.Printf("Failed to claim expired stream: %v", err)
				} else {
					log.Printf("✅ Successfully claimed expired stream")
				}

			case notifs.StreamDisowned:
				log.Printf("❌ Stream disowned: %v", notification.Payload)
				// Handle losing ownership of a stream
				handleStreamDisowned(notification.Payload.(string))

			default:
				log.Printf("Unknown notification type: %v", notification.Type)
			}
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	log.Println("Shutdown signal received, cleaning up...")

	// Graceful shutdown
	if err := client.Done(); err != nil {
		log.Printf("Error during cleanup: %v", err)
	} else {
		log.Println("Client cleanup completed successfully")
	}

	log.Println("Example completed")
}

// addTestData adds some test streams to the Load Balancer Stream (LBS)
func addTestData(ctx context.Context, redisClient redis.UniversalClient) {
	// Wait a bit for the client to be ready
	time.Sleep(2 * time.Second)

	log.Println("Adding test data to LBS...")

	for i := 0; i < 3; i++ {
		// Create a test message for the LBS
		lbsMessage := notifs.LBSMessage{
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
			log.Printf("Failed to marshal LBS message: %v", err)
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
			log.Printf("Failed to add message to LBS: %v", result.Err())
		} else {
			log.Printf("Added test message %d to LBS: %s", i, result.Val())
		}

		// Wait between messages
		time.Sleep(1 * time.Second)
	}
}

// handleStreamAdded processes a newly assigned stream
func handleStreamAdded(ctx context.Context, payload string) {
	var lbsMessage notifs.LBSMessage
	if err := json.Unmarshal([]byte(payload), &lbsMessage); err != nil {
		log.Printf("Failed to unmarshal LBS message: %v", err)
		return
	}

	log.Printf("Processing stream: %s", lbsMessage.DataStreamName)
	log.Printf("Stream info: %+v", lbsMessage.Info)

	// Simulate processing the data stream
	// In a real application, you would:
	// 1. Read from the actual data stream using XREAD or XREADGROUP
	// 2. Process the messages
	// 3. Acknowledge processed messages
	// 4. Call client.DoneStream() when finished with this stream

	log.Printf("✅ Finished processing stream: %s", lbsMessage.DataStreamName)
}

// handleStreamDisowned handles losing ownership of a stream
func handleStreamDisowned(payload string) {
	log.Printf("Lost ownership of stream, stopping processing: %s", payload)
	// In a real application, you would:
	// 1. Stop processing the stream
	// 2. Clean up any resources
	// 3. The stream will be claimed by another consumer
}
