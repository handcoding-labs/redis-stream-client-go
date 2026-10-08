package types

import (
	"context"

	"github.com/handcoding-labs/redis-stream-client-go/notifs"
)

// RedisStreamClient is an interface for a Redis Stream client
// This is the main interface for the Redis Stream client
type RedisStreamClient interface {
	// ID returns consumerID which uniquely identifies the consumer
	ID() string
	// Init Initialize the client
	//
	// Returns the load balanced stream (LBS) channel. This channel should be used by consumers
	// to find out which new data stream has been added for processing. Equivalent to kafka's topic.
	// 		   the key space notifications (ksp) channel. This channel should be used by consumers
	// to find out if any of the streams has expired. All notifications will come to kspchan.
	// 		   error if there is any in initialization
	Init(ctx context.Context) (outputChan <-chan notifs.RecoverableRedisNotification, err error)
	// Claim allows for a consumer to claim data stream from another failed consumer
	//
	// should be called once a consumer receives a message on kspchan
	Claim(ctx context.Context, kspNotification notifs.LBSInfo) error
	// Done marks the end of processing the stream
	//
	// should be called when consumer is shutting down and is not expected to be called again.
	//
	// Done does not close the Redis client passed to NewRedisStreamClient (the caller owns it), and
	// canceling the client's context does not interrupt its blocking read on the LBS stream. Until
	// that connection is closed the stopped consumer can still be handed one newly added LBS message,
	// which then waits in its pending list until the reconciliation scan re-queues it (see
	// docs/USAGE.md, "Shutdown and the Redis connection"). Close the Redis client after Done returns.
	Done(ctx context.Context) error
	// DoneStream marks end of processing for a particular stream
	//
	// should be called when consumer is done processing a particular data stream.
	DoneStream(ctx context.Context, dataStreamName string) error
	// ReinitTopology re-initializes the client against the cluster's current topology: it re-enables
	// keyspace notifications on the current masters and rebuilds the per-master subscriptions.
	//
	// It never changes the cluster's topology (no failover, no slot or node changes) and it does not
	// wait or retry; its only write is the keyspace-notification config that Init also applies. It first checks that
	// the cluster is settled, meaning every reachable node reports cluster_state:ok and they all agree
	// on which node owns which slots; if not it returns an error wrapping errs.ErrClusterNotSettled
	// and leaves the client as it was, and the caller decides when to try again.
	//
	// Only meaningful in ClusterModeOSS, where keyspace notifications fire per-master and the set
	// of masters can change after failover/resharding. Callers should invoke this when they detect
	// a topology change. It is a no-op in ClusterModeSingleShard.
	ReinitTopology(ctx context.Context) error
}
