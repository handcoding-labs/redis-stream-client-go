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
	ExpiredEventPattern             = "__keyevent@0__:expired"
	NotifyKeyspaceEventsCmd         = "notify-keyspace-events"
	KeyspacePatternForExpiredEvents = "Ex"
	RedisConsumerPrefix             = "redis-consumer-"
	PodName                         = "POD_NAME"
	PodIP                           = "POD_IP"
	LBSInput                        = "lbs-input"
	MutexKeySep                     = ":"
	DefaultLBSIdleTime              = 20 * DefaultHBInterval
	DefaultLBSRecoveryCount         = 1000
	DefaultHBInterval               = 2 * time.Second
)
