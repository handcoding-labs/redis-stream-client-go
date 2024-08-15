package impl

import (
	"bburli/redis-stream-client-go/types"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
)

// ReliableRedisStreamClient is an implementation of the RedisStreamClient interface
type ReliableRedisStreamClient struct {
	// underlying redis client used to interact with redis
	redisClient redis.UniversalClient
	// consumerID is the unique identifier for the consumer
	consumerID string
	// kspChan is the channel to read keyspace notifications
	kspChan <-chan *redis.Message
	// lbsChan is the channel to read messages from the LBS stream
	lbsChan chan *redis.XMessage
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
}

// NewRedisStreamClient creates a new RedisStreamClient
//
// This function creates a new RedisStreamClient with the given redis client and stream name
// Stream is the name of the stream to read from where actual data is transmitted
func NewRedisStreamClient(redisClient redis.UniversalClient, hbInterval time.Duration, serviceName string) types.RedisStreamClient {
	// obtain consumer name via kubernetes downward api
	podName := os.Getenv(types.PodName)
	podIP := os.Getenv(types.PodIP)

	if podName == "" && podIP == "" {
		panic("POD_NAME or POD_IP not set")
	}

	var consumerID string

	if len(podName) > 0 {
		consumerID = types.RedisConsumerPrefix + podName
	} else {
		consumerID = types.RedisConsumerPrefix + podIP
	}

	if hbInterval == 0 {
		// default to 1 second
		hbInterval = time.Second
	}

	return &ReliableRedisStreamClient{
		redisClient: redisClient,
		consumerID:  consumerID,
		kspChan:     make(<-chan *redis.Message, 500),
		lbsChan:     make(chan *redis.XMessage, 500),
		hbInterval:  hbInterval,
		streamLocks: make(map[string]*lbsInfo),
		serviceName: serviceName,
	}
}

// Init initializes the RedisStreamClient
//
// This function initializes the RedisStreamClient by enabling keyspace notifications for expired events,
// subscribing to expired events, and starting a blocking read on the LBS stream
// Returns a channel to read messages from the LBS stream. The client should read from this channel and process the messages.
// Returns a channel to read keyspace notifications. The client should read from this channel and process the notifications.
func (r *ReliableRedisStreamClient) Init(ctx context.Context) (<-chan *redis.XMessage, <-chan *redis.Message, error) {
	if r.checkErr(ctx, r.enableKeyspaceNotifsForExpiredEvents).
		checkErr(ctx, r.subscribeToExpiredEvents) == nil {
		return nil, nil, fmt.Errorf("error initializing the client")
	}

	lbsCtx, cancelFunc := context.WithCancel(ctx)
	r.lbsCtxCancelFunc = cancelFunc

	// start blocking read on LBS stream
	go r.readLBSStream(lbsCtx)

	return r.lbsChan, r.kspChan, nil
}

// Claim claims pending messages from a stream
func (r *ReliableRedisStreamClient) Claim(ctx context.Context, expiredStreamName string, newConsumerID string) error {
	// acquire lock on the stream
	parts := strings.Split(expiredStreamName, ":")
	streamName := parts[0]
	idInLBS := parts[1]

	pool := goredis.NewPool(r.redisClient)
	rs := redsync.New(pool)

	mutex := rs.NewMutex(expiredStreamName,
		redsync.WithExpiry(r.hbInterval),
		redsync.WithFailFast(true),
		redsync.WithRetryDelay(10*time.Millisecond),
		redsync.WithSetNXOnExtend(),
		redsync.WithGenValueFunc(func() (string, error) {
			return r.consumerID, nil
		}))

	// lock the stream
	if err := mutex.Lock(); err != nil {
		return err
	}

	_, err := mutex.Extend()
	if err != nil {
		return err
	}

	r.streamLocks[streamName] = &lbsInfo{
		DataStreamName: streamName,
		IDInLBS:        idInLBS,
		Mutex:          mutex,
	}

	// Claim the stream
	res := r.redisClient.XClaim(ctx, &redis.XClaimArgs{
		Stream:   r.lbsName(),
		Group:    r.lbsGroupName(),
		Consumer: newConsumerID,
		MinIdle:  0,
		Messages: []string{idInLBS},
	})

	if res.Err() != nil {
		return res.Err()
	}

	return nil
}

// Done marks the end of processing the stream
func (r *ReliableRedisStreamClient) Done(ctx context.Context, streamName string) error {
	lbsInfo, ok := r.streamLocks[streamName]
	if !ok {
		return fmt.Errorf("stream not found")
	}

	// unlock the stream
	ok, err := lbsInfo.Mutex.Unlock()
	log.Println("Unlocking stream", streamName, "done: ", ok, "err: ", err)
	if err != nil && !errors.Is(errors.Unwrap(err), redsync.ErrLockAlreadyExpired) {
		return err
	}

	// Acknowledge the message
	res := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), lbsInfo.IDInLBS)
	if res.Err() != nil {
		return res.Err()
	}

	// delete volatile key from streamLocks
	if ok {
		delete(r.streamLocks, streamName)
	}

	return nil
}

func (r *ReliableRedisStreamClient) Close() {
	close(r.lbsChan)
	err := r.pubSub.Close()
	if err != nil {
		log.Println("error in closing redis pub/sub")
	}
	// drain kspchan
	for range r.kspChan {
	}
}
