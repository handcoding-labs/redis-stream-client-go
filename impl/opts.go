package impl

import (
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
)

type RecoverableRedisOption func(*RecoverableRedisStreamClient)

// WithLBSIdleTime sets the time after which a message is considered idle and will be recovered
func WithLBSIdleTime(idleTime time.Duration) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) {
		// idleTime must be greater than 2 * heartbeat interval at least
		if idleTime != 0 && idleTime > (2*r.hbInterval) {
			r.lbsIdleTime = idleTime
		} else {
			// if the value is not valid, set it to the default
			r.lbsIdleTime = configs.DefaultLBSIdleTime
		}
	}
}

// WithLBSRecoveryCount sets the number of messages to fetch at a time during recovery
func WithLBSRecoveryCount(count int) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) {
		r.lbsRecoveryCount = count
	}
}
