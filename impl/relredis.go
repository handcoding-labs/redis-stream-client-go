package impl

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/metrics"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// StreamLocksInfo holds information needed to operation with data streams and their management for synchronization
type StreamLocksInfo struct {
	LBSInfo        notifs.LBSInfo
	RedsyncMutex   *redsync.Mutex
	AdditionalInfo map[string]any
}

// RecoverableRedisStreamClient is an implementation of the RedisStreamClient interface
type RecoverableRedisStreamClient struct {
	// underlying redis client used to interact with redis
	redisClient redis.UniversalClient
	// consumerID is the unique identifier for the consumer
	consumerID string
	// kspChan is the channel to read keyspace notifications
	kspChan chan *redis.Message
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
	// rs is a shared redsync instance used for distributed locks
	rs *redsync.Redsync
	// lbsIdleTime is the time after which a message is considered idle
	lbsIdleTime time.Duration
	// lbsRecoveryCount is the number of times a message is recovered
	lbsRecoveryCount int
	// kspChanSize is the size of kspChan corresponding to redis pub sub channel size
	kspChanSize int
	// outputChanSize is the size of the outputChan to which clients listen to
	outputChanSize int
	// kspChanTimeout is the duration after which a pub sub message from redis is dropped
	kspChanTimeout time.Duration
	// logger for plain json logging
	logger *slog.Logger
	// forceOverrideConfig indicates if the library should override existing keyspace notifications config
	forceOverrideConfig bool
	// NotificationBroker handles all messaging to clients
	notificationBroker *notifs.NotificationBroker
	// maxRetries is the maximum number of retries for LBS stream read errors
	maxRetries int
	// initialRetryDelay is the initial delay before retrying after an error
	initialRetryDelay time.Duration
	// maxRetryDelay is the maximum delay between retries
	maxRetryDelay time.Duration
	// metricsRecorder is used to record metrics related to Redis mutex operations and stream processing
	metricsRecorder metrics.Recorder
	// clusterMode selects the keyspace-notification and recovery strategy
	clusterMode ClusterMode
	// recoveryConfig configures the periodic reconciliation scan
	recoveryConfig RecoveryConfig
	// oss holds the per-master keyspace subscriptions opened in ClusterModeOSS
	oss ossSubscriptions
}

// NewRedisStreamClient creates a new RedisStreamClient
//
// This function creates a new RedisStreamClient with the given redis client and stream name
// Stream is the name of the stream to read from where actual data is transmitted
func NewRedisStreamClient(redisClient redis.UniversalClient, serviceName string,
	opts ...RecoverableRedisOption) (types.RedisStreamClient, error) {
	// obtain consumer name via kubernetes downward api
	podName := os.Getenv(configs.PodName)
	podIP := os.Getenv(configs.PodIP)

	if podName == "" && podIP == "" {
		return nil, errs.ErrPodConfigMissing
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
		redisClient:       redisClient,
		consumerID:        consumerID,
		kspChan:           make(chan *redis.Message, configs.DefaultKspChanSize),
		hbInterval:        configs.DefaultHBInterval,
		streamLocks:       make(map[string]*StreamLocksInfo),
		serviceName:       serviceName,
		outputChan:        make(chan notifs.RecoverableRedisNotification, configs.DefaultOutputChanSize),
		rs:                rs,
		lbsIdleTime:       configs.DefaultLBSIdleTime,
		lbsRecoveryCount:  configs.DefaultLBSRecoveryCount,
		kspChanSize:       configs.DefaultKspChanSize,
		kspChanTimeout:    configs.DefaultKspChanTimeout,
		logger:            slog.Default(),
		maxRetries:        configs.DefaultMaxRetries,
		initialRetryDelay: configs.DefaultInitialRetryDelay,
		maxRetryDelay:     configs.DefaultMaxRetryDelay,
		metricsRecorder:   &metrics.NoopRecorder{},
		clusterMode:       ClusterModeSingleShard,
		recoveryConfig:    DefaultRecoveryConfig(),
	}

	for _, opt := range opts {
		if err := opt(r); err != nil {
			return nil, err
		}
	}

	// Default the DLQ stream to a per-service name when the caller did not configure one, so that
	// poison messages (those exceeding MaxRetries) are preserved on a dedicated stream rather than
	// dropped. The DLQ lives on the same logical keyspace as the LBS stream.
	if r.recoveryConfig.DLQStream == "" {
		r.recoveryConfig.DLQStream = r.lbsName() + configs.DLQSuffix
	}

	// init the notification broker
	r.notificationBroker = notifs.NewNotificationBroker(r.outputChan, configs.DefaultOutputChanSize)

	return r, nil
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
func (r *RecoverableRedisStreamClient) Init(ctx context.Context) (<-chan notifs.RecoverableRedisNotification, error) {
	// ClusterModeOSS requires a cluster client so that we can subscribe to keyspace notifications
	// on every master and reload topology on failover/resharding.
	if r.clusterMode == ClusterModeOSS {
		if _, ok := r.redisClient.(*redis.ClusterClient); !ok {
			return nil, errs.ErrClusterClientRequired
		}
	}

	keyspaceErr := r.enableKeyspaceNotifsForExpiredEvents(ctx)
	if keyspaceErr != nil {
		return nil, keyspaceErr
	}

	// start listening to redis pub sub
	r.subscribeToExpiredEvents(ctx)

	newCtx, cancelFunc := context.WithCancel(ctx)
	r.lbsCtxCancelFunc = cancelFunc

	// create group
	res := r.redisClient.XGroupCreateMkStream(ctx, r.lbsName(), r.lbsGroupName(), configs.StartFromNow)
	if err := res.Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, errs.NewRedisError(errs.OpCreateLBSStream, err)
	}

	// start blocking read on LBS stream
	go r.readLBSStream(newCtx)

	// listen to ksp chan
	go r.listenKsp(newCtx)

	// start the periodic reconciliation scan which recovers pending LBS messages whose owning
	// consumer has died. The first pass runs immediately (replacing the old startup recovery).
	go r.runReconciliationLoop(newCtx)

	return r.outputChan, nil
}

// Claim recovers a data stream whose owning consumer is presumed dead.
//
// It is called by clients in response to a StreamExpired notification. Rather than taking direct
// ownership via XCLAIM (which is not cluster-safe and bypasses the lock-liveness check), Claim
// acknowledges the dead consumer's pending LBS message and re-adds the task as a brand-new message
// (XACK + XADD) so it is redistributed normally through XREADGROUP. The consumer that subsequently
// reads it — possibly this one — acquires the lock and receives a StreamAdded notification.
//
// Because the re-queue is gated on the XACK winning the race, concurrent Claim calls for the same
// expired stream are de-duplicated: only one consumer re-adds the task. The others receive
// ErrAlreadyClaimed.
func (r *RecoverableRedisStreamClient) Claim(ctx context.Context, lbsInfo notifs.LBSInfo) error {
	r.logger.Info("claiming stream via re-queue", "consumer_id", r.consumerID,
		"mutex_key", lbsInfo.FormMutexKey(), "timestamp", time.Now().Format(time.RFC3339))

	outcome, _, err := r.reQueue(ctx, lbsInfo.IDInLBS)
	if err != nil {
		return err
	}

	// If we did not win the XACK race (someone else already recovered it) or the lock is still
	// held by a live consumer, there is nothing for this caller to do.
	if outcome != reQueueRequeued && outcome != reQueueDLQ {
		return errs.ErrAlreadyClaimed
	}

	return nil
}

// DoneStream marks end of processing for a particular stream
//
// This function is used to mark the end of processing for a particular stream
// It unlocks the stream, acknowledges the message and deletes the message from the stream.
func (r *RecoverableRedisStreamClient) DoneStream(ctx context.Context, dataStreamName string) error {
	streamLocksInfo, err := r.popStreamLocksInfo(dataStreamName)
	if err != nil {
		return err
	}

	// unlock the stream
	_, err = streamLocksInfo.RedsyncMutex.Unlock()
	if err != nil && !errors.Is(errors.Unwrap(err), redsync.ErrLockAlreadyExpired) {
		r.logger.Error("error unlocking stream", "error", err.Error())
		r.metricsRecorder.RecordLockReleaseAttempt(dataStreamName, false)
		return errs.NewMutexError(errs.OpUnlockMutex, err)
	}
	r.metricsRecorder.RecordLockReleaseAttempt(dataStreamName, true)

	// Acknowledge the message
	res := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), streamLocksInfo.LBSInfo.IDInLBS)
	if res.Err() != nil {
		r.logger.Error("error acking stream", "error", res.Err())
		return errs.NewRedisError(errs.OpAckStream, err)
	}

	// Delete the message from the stream
	res = r.redisClient.XDel(ctx, r.lbsName(), streamLocksInfo.LBSInfo.IDInLBS)
	if res.Err() != nil {
		r.logger.Error("error deleting stream", "error", res.Err())
		return errs.NewRedisError(errs.OpDelStream, res.Err())
	}

	r.metricsRecorder.RecordStreamProcessingEnd(dataStreamName, time.Now())
	return nil
}

// Done marks the end of processing for a client
//
// Note that done is called when the client is shutting down and is not expected to be called again
// It cleans up all the streams handled by the client
// To cleanup a specific stream, use DoneStream
func (r *RecoverableRedisStreamClient) Done(ctx context.Context) error {
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

// ResetTopology re-derives the cluster topology and re-establishes keyspace subscriptions.
//
// In ClusterModeOSS, keyspace notifications fire only on the master that owns the expiring key, so
// the client subscribes to every master. After a failover or resharding the set of masters changes;
// callers should invoke ResetTopology to reload the cluster state and re-subscribe to the current
// masters. In ClusterModeSingleShard this is a no-op.
func (r *RecoverableRedisStreamClient) ResetTopology(ctx context.Context) error {
	if r.clusterMode != ClusterModeOSS {
		return nil
	}

	cluster, ok := r.redisClient.(*redis.ClusterClient)
	if !ok {
		r.metricsRecorder.RecordTopologyReset(false)
		return errs.ErrClusterClientRequired
	}

	// reload the cluster's view of the topology (failover / resharding)
	cluster.ReloadState(ctx)

	// re-enable keyspace notifications on (possibly new) masters
	if err := r.enableKeyspaceNotifsForExpiredEvents(ctx); err != nil {
		r.metricsRecorder.RecordTopologyReset(false)
		return err
	}

	// tear down existing per-master subscriptions and re-subscribe to the current masters
	r.oss.closeAll(r.logger)
	r.subscribeToExpiredEvents(ctx)

	r.metricsRecorder.RecordTopologyReset(true)
	r.logger.Info("cluster topology reset and keyspace subscriptions rebuilt", "consumer_id", r.consumerID)
	return nil
}
