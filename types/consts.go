package types

const (
	GroupSuffix                     = "-group"
	InputSuffix                     = "-input"
	PendingMsgID                    = ">"
	StartFromNow                    = "$"
	ExpiredEventPattern             = "__keyevent@0__:expired"
	NotifyKeyspaceEventsCmd         = "notify-keyspace-events"
	KeyspacePatternForExpiredEvents = "Ex"
	RedisConsumerPrefix             = "redis-consumer-"
	PodName                         = "POD_NAME"
	PodIP                           = "POD_IP"
	LBSInput                        = "lbs-input"
	MutexKeySep                     = ":"
	LockAlreadyTakenErrMsg          = "lock already taken"
)
