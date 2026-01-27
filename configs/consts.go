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
	ExpiredEventPattern             = "__keyevent@0__:expired"
	NotifyKeyspaceEventsCmd         = "notify-keyspace-events"
	KeyspacePatternForExpiredEvents = "Ex"
	RedisConsumerPrefix             = "redis-consumer-"
	PodName                         = "POD_NAME"
	PodIP                           = "POD_IP"
	LBSInput                        = "lbs-input"
	MutexKeySep                     = "<MUTEX_KEY_SEP>"
	DefaultLBSIdleTime              = 20 * DefaultHBInterval
	DefaultLBSRecoveryCount         = 1000
	DefaultHBInterval               = 2 * time.Second
	DefaultKspChanSize              = 100
	DefaultKspChanTimeout           = 10 * time.Minute
	DefaultOutputChanSize           = 500
	DefaultMaxRetries               = 5
	DefaultInitialRetryDelay        = 100 * time.Millisecond
	DefaultMaxRetryDelay            = 30 * time.Second
)
