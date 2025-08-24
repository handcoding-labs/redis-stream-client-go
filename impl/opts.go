package impl

import "time"

type RecoverableRedisOption func(*RecoverableRedisStreamClient)

// WithLBSIdleTime sets the time after which a message is considered idle and will be recovered
func WithLBSIdleTime(idleTime time.Duration) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) {
		r.lbsIdleTime = idleTime
	}
}

// WithLBSRecoveryCount sets the number of messages to fetch at a time during recovery
func WithLBSRecoveryCount(count int) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) {
		r.lbsRecoveryCount = count
	}
}
