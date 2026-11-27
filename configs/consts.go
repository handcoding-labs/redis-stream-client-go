package configs

import "time"

const (
	GroupSuffix                     = "-group"
	InputSuffix                     = "-input"
	PendingMsgID                    = ">"
	StartFromNow                    = "$"
	MinimalRangeID                  = "-"
	MaximalRangeID                  = "+"
	StartID                         = "0"
	StartIDPair                     = "0-0"
	KeySpacePrefix                  = "__keyspace@0__:"
	ExpiredPayload                  = "expired"
	MutexKeySpacePattern            = KeySpacePrefix + "*" + MutexKeySep + "*" // pattern for expired events of mutex keys
	NotifyKeyspaceEventsCmd         = "notify-keyspace-events"
	KeyspacePatternForExpiredEvents = "KEx"
	RedisConsumerPrefix             = "redis-consumer-"
	PodName                         = "POD_NAME"
	PodIP                           = "POD_IP"
	LBSInput                        = "lbs-input"
	MutexKeySep                     = "<MUTEX_KEY_SEP>"
	DefaultLBSIdleTime              = 20 * DefaultHBInterval
	DefaultLBSRecoveryCount         = 1000
	DefaultHBInterval               = 2 * time.Second
	DefaultKspChanSize              = 500
	DefaultKspChanTimeout           = 10 * time.Minute
	DefaultOutputChanSize           = 500
	DefaultMaxRetries               = 5
	DefaultInitialRetryDelay        = 100 * time.Millisecond
	DefaultMaxRetryDelay            = 30 * time.Second

	// DefaultLBSReadBlock bounds each blocking XREADGROUP on the LBS stream when the read cannot be
	// interrupted with CLIENT UNBLOCK (the command is not permitted, or the client type cannot pin a
	// connection). go-redis does not interrupt a blocking read when its context is canceled, so this
	// is then the upper bound on how long Done waits for the read to notice cancellation.
	DefaultLBSReadBlock = DefaultHBInterval

	// DefaultLBSInterruptibleReadBlock bounds each blocking XREADGROUP when the read can be
	// interrupted with CLIENT UNBLOCK. Shutdown does not wait for it; it only limits how long a read
	// that missed every unblock attempt can stay parked.
	DefaultLBSInterruptibleReadBlock = 30 * time.Second

	// LBSUnblockRetryInterval is how often CLIENT UNBLOCK is repeated after cancellation until the
	// reader has exited.
	LBSUnblockRetryInterval = 50 * time.Millisecond
)

// Reconciliation / recovery stream field names and DLQ markers (cluster support).
const (
	// RetryCountField is the stream field that tracks how many times a message
	// has been re-queued by the reconciliation scan or Claim.
	RetryCountField = "_retry_count"
	// DLQReasonField is the field added to messages routed to the DLQ stream.
	DLQReasonField = "_dlq_reason"
	// DLQReasonMaxRetries is the reason recorded when a message exceeds MaxRetries.
	DLQReasonMaxRetries = "max_retries_exceeded"
	// DLQReasonUnparseable is the reason recorded when a message cannot be parsed into a valid LBS
	// message and is therefore routed to the DLQ for inspection rather than retried.
	DLQReasonUnparseable = "unparseable"
	// DLQSuffix is appended to the LBS stream name to form the default DLQ stream name when the
	// caller does not configure one explicitly.
	DLQSuffix = "-dlq"
)

// Reconciliation / recovery default tunables (cluster support).
const (
	// DefaultReconciliationInterval is the base period of the periodic recovery scan.
	DefaultReconciliationInterval = 60 * time.Second
	// DefaultMinIdleTime is the minimum idle time before a pending message is eligible for recovery.
	DefaultMinIdleTime = 30 * time.Second
	// DefaultReconciliationBatchSize is the max number of pending messages inspected per scan.
	DefaultReconciliationBatchSize = 50
	// DefaultMaxReQueueRetries is the default number of re-queue attempts before DLQ routing.
	DefaultMaxReQueueRetries = 3
	// DefaultJitterFraction is the fraction of the reconciliation interval used as random jitter.
	DefaultJitterFraction = 0.1
	// DefaultDLQMaxLen caps the DLQ stream length by default (approximate MAXLEN trim) so it does
	// not grow unbounded, since nothing acknowledges or trims it. Set RecoveryConfig.DLQMaxLen to 0
	// to disable the cap and retain every routed message.
	DefaultDLQMaxLen = 10000
)
