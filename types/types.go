package types

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// RedisStreamClient is an interface for a Redis Stream client
// This is the main interface for the Redis Stream client
type RedisStreamClient interface {
	// Initialize the client
	Init(ctx context.Context) (chan *redis.XMessage, <-chan *redis.Message, error)
	// ClaimStream allows for a consumer to claim data stream from another failed consumer
	Claim(ctx context.Context, streamName string, newConsumerID string) error
	// Done marks the end of processing the stream
	Done(ctx context.Context, streamName string) error
}
