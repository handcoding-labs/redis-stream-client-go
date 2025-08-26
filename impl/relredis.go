package impl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types"
)

// RecoverableRedisStreamClient is an implementation of the RedisStreamClient interface
type RecoverableRedisStreamClient struct {
	// underlying redis client used to interact with redis
	redisClient redis.UniversalClient
	// consumerID is the unique identifier for the consumer
	consumerID string
	// kspChan is the channel to read keyspace notifications
	kspChan <-chan *redis.Message
	// lbsCtxCancelFunc is used to control when to kill go routines spwaned as part of lbs
	lbsCtxCancelFunc context.CancelFunc
	// hbInterval is the interval at which the client sends heartbeats
	hbInterval time.Duration
	// streamLocks is a map of stream name to LBSInfo for locking
	streamLocks map[string]*lbsInfo
	// serviceName is the name of the service
	serviceName string
	// redis pub sub subscription
	pubSub *redis.PubSub
	// outputChan is the channel exposed to clients on which we relay all messages
	outputChan chan notifs.RecoverableRedisNotification[any]
	// outputChanClosed tracks if the outputChan is still being read by client
	outputChanClosed atomic.Bool
	// rs is a shared redsync instance used for distributed locks
	rs *redsync.Redsync
	// lbsIdleTime is the time after which a message is considered idle
	lbsIdleTime time.Duration
	// lbsRecoveryCount is the number of times a message is recovered
	lbsRecoveryCount int
}

// NewRedisStreamClient creates a new RedisStreamClient
//
// This function creates a new RedisStreamClient with the given redis client and stream name
// Stream is the name of the stream to read from where actual data is transmitted
func NewRedisStreamClient(redisClient redis.UniversalClient, serviceName string, opts ...RecoverableRedisOption) types.RedisStreamClient {
	// obtain consumer name via kubernetes downward api
	podName := os.Getenv(configs.PodName)
	podIP := os.Getenv(configs.PodIP)

	if podName == "" && podIP == "" {
		panic("POD_NAME or POD_IP not set")
	}

	var consumerID string

	if len(podName) > 0 {
		consumerID = configs.RedisConsumerPrefix + podName
	} else {
		consumerID = configs.RedisConsumerPrefix + podIP
	}

	pool := goredis.NewPool(redisClient)
	rs := redsync.New(pool)

	r := &RecoverableRedisStreamClient{
		redisClient:      redisClient,
		consumerID:       consumerID,
		kspChan:          make(chan *redis.Message, 500),
		hbInterval:       configs.DefaultHBInterval,
		streamLocks:      make(map[string]*lbsInfo),
		serviceName:      serviceName,
		outputChan:       make(chan notifs.RecoverableRedisNotification[any], 500),
		outputChanClosed: atomic.Bool{},
		rs:               rs,
		lbsIdleTime:      configs.DefaultLBSIdleTime,
		lbsRecoveryCount: configs.DefaultLBSRecoveryCount,
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// ID returns the consumer name that uniquely identifies the consumer
func (r *RecoverableRedisStreamClient) ID() string {
	return r.consumerID
}

// Init initializes the RedisStreamClient
//
// This function initializes the RedisStreamClient by enabling keyspace notifications for expired events,
// subscribing to expired events, and starting a blocking read on the LBS stream
// Returns a channel to read messages from the LBS stream. The client should read from this channel and process the messages.
// Returns a channel to read keyspace notifications. The client should read from this channel and process the notifications.
func (r *RecoverableRedisStreamClient) Init(ctx context.Context) (<-chan notifs.RecoverableRedisNotification[any], error) {
	if r.checkErr(ctx, r.enableKeyspaceNotifsForExpiredEvents).
		checkErr(ctx, r.subscribeToExpiredEvents) == nil {
		return nil, fmt.Errorf("error initializing the client")
	}

	lbsCtx, cancelFunc := context.WithCancel(ctx)
	r.lbsCtxCancelFunc = cancelFunc

	// create group
	res := r.redisClient.XGroupCreateMkStream(ctx, r.lbsName(), r.lbsGroupName(), configs.StartFromNow)
	if res.Err() != nil && !strings.Contains(res.Err().Error(), "BUSYGROUP") {
		return nil, res.Err()
	}

	// recovery of unacked LBS messages
	r.recoverUnackedLBS(lbsCtx)

	// start blocking read on LBS stream
	go r.readLBSStream(lbsCtx)

	// listen to ksp chan
	go r.listenKsp()

	return r.outputChan, nil
}

// Claim claims pending messages from a stream
func (r *RecoverableRedisStreamClient) Claim(ctx context.Context, mutexKey string) error {
	lbsInfo, err := createByMutexKey(mutexKey)
	if err != nil {
		return err
	}

	// Claim the stream
	res := r.redisClient.XClaim(ctx, &redis.XClaimArgs{
		Stream:   r.lbsName(),
		Group:    r.lbsGroupName(),
		Consumer: r.consumerID,
		MinIdle:  r.hbInterval, // one heartbeat interval has elapsed
		Messages: []string{lbsInfo.IDInLBS},
	})

	if res.Err() != nil {
		return res.Err()
	}

	claimed, err := res.Result()
	if err != nil {
		return err
	}

	if len(claimed) == 0 {
		return fmt.Errorf("already claimed")
	}

	mutex := r.rs.NewMutex(lbsInfo.getMutexKey(),
		redsync.WithExpiry(r.hbInterval),
		redsync.WithFailFast(true),
		redsync.WithRetryDelay(10*time.Millisecond),
		redsync.WithSetNXOnExtend(),
		redsync.WithGenValueFunc(func() (string, error) {
			return r.consumerID, nil
		}))

	// lock once
	if err := mutex.Lock(); err != nil {
		return err
	}

	go r.startExtendingKey(ctx, mutex, lbsInfo.DataStreamName)

	// seed the mutex
	lbsInfo.Mutex = mutex
	r.streamLocks[lbsInfo.DataStreamName] = lbsInfo

	return nil
}

// DoneStream marks end of processing for a particular stream
//
// This function is used to mark the end of processing for a particular stream
// It unlocks the stream and acknowledges the message and cleans up internal state
func (r *RecoverableRedisStreamClient) DoneStream(ctx context.Context, dataStreamName string) error {
	lbsInfo, ok := r.streamLocks[dataStreamName]
	if !ok {
		return fmt.Errorf("stream not found")
	}

	// delete volatile key from streamLocks
	if ok {
		delete(r.streamLocks, dataStreamName)
	}

	// unlock the stream
	_, err := lbsInfo.Mutex.Unlock()
	if err != nil && !errors.Is(errors.Unwrap(err), redsync.ErrLockAlreadyExpired) {
		return err
	}

	// Acknowledge the message
	res := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), lbsInfo.IDInLBS)
	if res.Err() != nil {
		return res.Err()
	}

	return nil
}

// Done marks the end of processing for a client
//
// Note that done is called when the client is shutting down and is not expected to be called again
// It cleans up all the streams handled by the client
// To cleanup a specific stream, use DoneStream
func (r *RecoverableRedisStreamClient) Done() error {
	ctx := context.Background()

	for streamName := range r.streamLocks {
		if err := r.DoneStream(ctx, streamName); err != nil {
			return err
		}
	}

	// release resources
	if err := r.cleanup(); err != nil {
		return err
	}

	return nil
}

func (r *RecoverableRedisStreamClient) cleanup() error {
	if err := r.pubSub.Close(); err != nil {
		return err
	}

	// drain kspchan and ignore expired notifications
	// since client has called Done and thus are no longer interested in expired notifications
	for len(r.kspChan) > 0 {
		<-r.kspChan
	}

	// cancel LBS context
	r.lbsCtxCancelFunc()

	// close the output channel
	if r.outputChanClosed.CompareAndSwap(false, true) {
		close(r.outputChan)
	}

	return nil
}
