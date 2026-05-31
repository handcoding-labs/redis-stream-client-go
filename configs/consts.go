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
)
