package impl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types"
)

// StreamLocksInfo holds information needed to operation with data streams and their management for synchronization
type StreamLocksInfo struct {
	LBSInfo        notifs.LBSInfo
	Mutex          *redsync.Mutex
	AdditionalInfo map[string]any
}

// RecoverableRedisStreamClient is an implementation of the RedisStreamClient interface
type RecoverableRedisStreamClient struct {
	// underlying redis client used to interact with redis
	redisClient redis.UniversalClient
	// consumerID is the unique identifier for the consumer
	consumerID string
	// kspChan is the channel to read keyspace notifications
	kspChan <-chan *redis.Message
	// lbsCtxCancelFunc is used to control when to kill go routines spawned as part of lbs
	lbsCtxCancelFunc context.CancelFunc
	// hbInterval is the interval at which the client sends heartbeats
	hbInterval time.Duration
	// streamLocks is a map of stream name to LBSInfo for locking
	streamLocks map[string]*StreamLocksInfo
	// streamLocksMutex protects streamLocks map from concurrent access
	streamLocksMutex sync.RWMutex
	// serviceName is the name of the service
	serviceName string
	// redis pub sub subscription
	pubSub *redis.PubSub
	// outputChan is the channel exposed to clients on which we relay all messages
	outputChan chan notifs.RecoverableRedisNotification
	// quitChan tracks if the outputChan is closed
	quitChan chan struct{}
	// wg ensures that cleanup can wait till all senders to outputChan are done
	wg sync.WaitGroup
	// rs is a shared redsync instance used for distributed locks
	rs *redsync.Redsync
	// lbsIdleTime is the time after which a message is considered idle
	lbsIdleTime time.Duration
	// lbsRecoveryCount is the number of times a message is recovered
	lbsRecoveryCount int
	// kspChanSize is the size of kspChan corresponding to redis pub sub channel size
	kspChanSize int
	// kspChanTimeout is the duration after which a pub sub message from redis is dropped
	kspChanTimeout time.Duration
	// logger for plain json logging
	logger *slog.Logger
}

// NewRedisStreamClient creates a new RedisStreamClient
//
// This function creates a new RedisStreamClient with the given redis client and stream name
// Stream is the name of the stream to read from where actual data is transmitted
func NewRedisStreamClient(
	redisClient redis.UniversalClient,
	serviceName string,
	opts ...RecoverableRedisOption,
) types.RedisStreamClient {
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
		streamLocks:      make(map[string]*StreamLocksInfo),
		serviceName:      serviceName,
		outputChan:       make(chan notifs.RecoverableRedisNotification, 500),
		quitChan:         make(chan struct{}),
		wg:               sync.WaitGroup{},
		rs:               rs,
		lbsIdleTime:      configs.DefaultLBSIdleTime,
		lbsRecoveryCount: configs.DefaultLBSRecoveryCount,
		kspChanSize:      configs.DefaultKspChanSize,
		kspChanTimeout:   configs.DefaultKspChanTimeout,
		logger:           getGoogleCloudLogger(),
	}

	for _, opt := range opts {
		if err := opt(r); err != nil {
			panic(fmt.Sprintf("invalid option: %v", err))
		}
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
// Returns a channel to read messages from the LBS stream. The client should read from this channel and
// process the messages.
func (r *RecoverableRedisStreamClient) Init(
	ctx context.Context,
) (<-chan notifs.RecoverableRedisNotification, error) {
	keyspaceErr := r.enableKeyspaceNotifsForExpiredEvents(ctx)
	if keyspaceErr != nil {
		return nil, fmt.Errorf("error while enabling keyspace notifications for expired events: %w", keyspaceErr)
	}

	expiredErr := r.subscribeToExpiredEvents(ctx)
	if expiredErr != nil {
		return nil, fmt.Errorf("error while subscribing to expired events: %w", expiredErr)
	}

	newCtx, cancelFunc := context.WithCancel(ctx)
	r.lbsCtxCancelFunc = cancelFunc

	// create group
	res := r.redisClient.XGroupCreateMkStream(ctx, r.lbsName(), r.lbsGroupName(), configs.StartFromNow)
	if res.Err() != nil && !strings.Contains(res.Err().Error(), "BUSYGROUP") {
		return nil, res.Err()
	}

	// recovery of unacked LBS messages
	r.recoverUnackedLBS(newCtx)

	// start blocking read on LBS stream
	go r.readLBSStream(newCtx)

	// listen to ksp chan
	r.wg.Add(1)
	go r.listenKsp(newCtx)

	return r.outputChan, nil
}

// Claim claims pending messages from a stream
func (r *RecoverableRedisStreamClient) Claim(ctx context.Context, lbsInfo notifs.LBSInfo) error {
	r.logger.Info("claiming stream", "consumer_id", r.consumerID, "mutex_key", lbsInfo,
		"timestamp", time.Now().Format(time.RFC3339))

	// Claim the stream
	res := r.redisClient.XClaim(ctx, &redis.XClaimArgs{
		Stream:   r.lbsName(),
		Group:    r.lbsGroupName(),
		Consumer: r.consumerID,
		MinIdle:  r.hbInterval, // one heartbeat interval must have elapsed
		Messages: []string{lbsInfo.IDInLBS},
	})

	if res.Err() != nil {
		r.logger.Error("error claiming stream", "error", res.Err(), "consumer_id", r.consumerID)
		return res.Err()
	}

	claimed, err := res.Result()
	if err != nil {
		r.logger.Error("error getting claimed stream", "error", err, "consumer_id", r.consumerID,
			"mutex_key", lbsInfo.FormMutexKey())
		return err
	}

	if len(claimed) == 0 {
		return fmt.Errorf("already claimed")
	}

	mutex := r.rs.NewMutex(lbsInfo.FormMutexKey(),
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

	// Retrieve the original message to get AdditionalInfo
	var additionalInfo map[string]any
	if len(claimed) > 0 {
		if lbsInputStr, ok := claimed[0].Values[configs.LBSInput].(string); ok {
			var lbsMessage notifs.LBSInputMessage
			if err := json.Unmarshal([]byte(lbsInputStr), &lbsMessage); err == nil {
				additionalInfo = lbsMessage.Info
			}
		}
	}

	go func() {
		if err := r.startExtendingKey(ctx, mutex, lbsInfo, additionalInfo); err != nil {
			r.logger.Error("Error extending key", "error", err, "stream", lbsInfo.DataStreamName, "consumer_id", r.consumerID)
		}
	}()

	// populate StreamLocks info
	r.streamLocksMutex.Lock()
	r.streamLocks[lbsInfo.DataStreamName] = &StreamLocksInfo{
		LBSInfo:        lbsInfo,
		Mutex:          mutex,
		AdditionalInfo: additionalInfo,
	}
	r.streamLocksMutex.Unlock()

	return nil
}

// DoneStream marks end of processing for a particular stream
//
// This function is used to mark the end of processing for a particular stream
// It unlocks the stream and acknowledges the message and cleans up internal state
func (r *RecoverableRedisStreamClient) DoneStream(ctx context.Context, dataStreamName string) error {
	r.streamLocksMutex.Lock()
	streamLocksInfo, ok := r.streamLocks[dataStreamName]
	if !ok {
		r.streamLocksMutex.Unlock()
		return fmt.Errorf("stream not found")
	}

	// delete volatile key from streamLocks
	delete(r.streamLocks, dataStreamName)
	r.streamLocksMutex.Unlock()

	// unlock the stream
	_, err := streamLocksInfo.Mutex.Unlock()
	if err != nil && !errors.Is(errors.Unwrap(err), redsync.ErrLockAlreadyExpired) {
		return err
	}

	// Acknowledge the message
	res := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), streamLocksInfo.LBSInfo.IDInLBS)
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

	// Get all stream names first to avoid holding lock during DoneStream calls
	r.streamLocksMutex.RLock()
	streamNames := make([]string, 0, len(r.streamLocks))
	for streamName := range r.streamLocks {
		streamNames = append(streamNames, streamName)
	}
	r.streamLocksMutex.RUnlock()

	for _, streamName := range streamNames {
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

	// close the output channel
	r.closeOutputChan()

	// cancel LBS context
	r.lbsCtxCancelFunc()

	return nil
}
