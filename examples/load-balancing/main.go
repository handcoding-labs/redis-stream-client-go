package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/impl"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
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

	log.Printf("Starting consumer: %s", consumerID)

	// Create Redis client
	redisClient := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs: []string{"localhost:6379"},
		DB:    0,
	})
	defer redisClient.Close()

	// Test Redis connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Enable keyspace notifications
	if err := redisClient.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
		log.Fatalf("Failed to enable keyspace notifications: %v", err)
	}

	// Create Redis Stream Client
	client := impl.NewRedisStreamClient(redisClient, "load-balance-demo")
	log.Printf("Created client with ID: %s", client.ID())

	// Initialize the client
	outputChan, err := client.Init(ctx)
	if err != nil {
		log.Fatalf("Failed to initialize client: %v", err)
	}

	// Track processed streams
	var processedStreams sync.Map
	var streamCount int32

	// Process notifications
	go func() {
		for notification := range outputChan {
			switch notification.Type {
			case notifs.StreamAdded:
				log.Printf("🎉 [%s] New stream assigned", consumerID)
				go handleStreamAdded(ctx, client, notification.Payload.(string), &processedStreams, &streamCount)

			case notifs.StreamExpired:
				log.Printf("⚠️  [%s] Stream expired, attempting to claim: %v", consumerID, notification.Payload)
				if err := client.Claim(ctx, notification.Payload.(string)); err != nil {
					log.Printf("❌ [%s] Failed to claim expired stream: %v", consumerID, err)
				} else {
					log.Printf("✅ [%s] Successfully claimed expired stream", consumerID)
					go handleClaimedStream(ctx, client, notification.Payload.(string), &processedStreams, &streamCount)
				}

			case notifs.StreamDisowned:
				log.Printf("❌ [%s] Stream disowned: %v", consumerID, notification.Payload)
			}
		}
	}()

	// Print statistics periodically
	go printStatistics(ctx, consumerID, &processedStreams, &streamCount)

	// Wait for shutdown signal
	<-sigChan
	log.Printf("🛑 [%s] Shutdown signal received, cleaning up...", consumerID)

	// Graceful shutdown
	if err := client.Done(); err != nil {
		log.Printf("❌ [%s] Error during cleanup: %v", consumerID, err)
	} else {
		log.Printf("✅ [%s] Client cleanup completed", consumerID)
	}
}

func runProducer() {
	ctx := context.Background()

	log.Println("🏭 Starting producer...")

	// Create Redis client
	redisClient := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs: []string{"localhost:6379"},
		DB:    0,
	})
	defer redisClient.Close()

	// Test connection
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Produce messages continuously
	messageID := 0
	for {
		// Create batch of messages
		for i := 0; i < 5; i++ {
			lbsMessage := notifs.LBSMessage{
				DataStreamName: fmt.Sprintf("order-stream-%d", messageID),
				Info: map[string]interface{}{
					"order_id":     fmt.Sprintf("order-%d", messageID),
					"customer_id":  fmt.Sprintf("customer-%d", messageID%100),
					"amount":       float64(messageID%1000 + 100),
					"created_at":   time.Now().Format(time.RFC3339),
					"status":       "pending",
					"priority":     []string{"low", "normal", "high"}[messageID%3],
				},
			}

			messageData, err := json.Marshal(lbsMessage)
			if err != nil {
				log.Printf("❌ Failed to marshal message: %v", err)
				continue
			}

			result := redisClient.XAdd(ctx, &redis.XAddArgs{
				Stream: "load-balance-demo-input",
				Values: map[string]interface{}{
					"lbs-input": string(messageData),
				},
			})

			if result.Err() != nil {
				log.Printf("❌ Failed to add message: %v", result.Err())
			} else {
				log.Printf("📤 Produced message %d: %s", messageID, lbsMessage.DataStreamName)
			}

			messageID++
		}

		// Wait before next batch
		time.Sleep(3 * time.Second)
	}
}

func handleStreamAdded(ctx context.Context, client *impl.RecoverableRedisStreamClient, payload string, processedStreams *sync.Map, streamCount *int32) {
	var lbsMessage notifs.LBSMessage
	if err := json.Unmarshal([]byte(payload), &lbsMessage); err != nil {
		log.Printf("❌ Failed to unmarshal LBS message: %v", err)
		return
	}

	consumerID := client.ID()
	streamName := lbsMessage.DataStreamName

	log.Printf("🔄 [%s] Processing stream: %s", consumerID, streamName)
	log.Printf("📋 [%s] Stream details: %+v", consumerID, lbsMessage.Info)

	// Simulate processing time (varies by priority)
	processingTime := 2 * time.Second
	if priority, ok := lbsMessage.Info["priority"].(string); ok {
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

	// In a real application, you would call client.DoneStream() when finished
	// For this demo, we'll simulate completion
	log.Printf("✅ [%s] Completed processing stream: %s (took %v)", consumerID, streamName, processingTime)
}

func handleClaimedStream(ctx context.Context, client *impl.RecoverableRedisStreamClient, payload string, processedStreams *sync.Map, streamCount *int32) {
	// Extract stream name from the payload (format: "stream_name:message_id")
	// For this demo, we'll just log that we claimed it
	log.Printf("🔄 [%s] Processing claimed stream: %s", client.ID(), payload)

	// Simulate processing the claimed stream
	time.Sleep(1 * time.Second)

	processedStreams.Store(payload, time.Now())
	*streamCount++

	log.Printf("✅ [%s] Completed processing claimed stream: %s", client.ID(), payload)
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
			log.Printf("📊 [%s] Statistics: Processed %d streams", consumerID, count)

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
				log.Printf("📈 [%s] Recent activity: %d streams in last 10 seconds", consumerID, recentCount)
			}
		}
	}
}
