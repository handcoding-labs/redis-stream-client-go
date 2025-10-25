package impl

import (
	"fmt"
	"time"
)

type RecoverableRedisOption func(*RecoverableRedisStreamClient) error

// WithLBSIdleTime sets the time after which a message is considered idle and will be recovered
func WithLBSIdleTime(idleTime time.Duration) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		// idleTime must be greater than 2 * heartbeat interval at least
		if idleTime == 0 || idleTime < (2*r.hbInterval) {
			return fmt.Errorf("idleTime must be greater than 2 * heartbeat interval at least")
		}

		r.lbsIdleTime = idleTime
		return nil
	}
}

// WithLBSRecoveryCount sets the number of messages to fetch at a time during recovery
func WithLBSRecoveryCount(count int) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if count <= 0 {
			return fmt.Errorf("recovery count must be greater than 0")
		}

		r.lbsRecoveryCount = count
		return nil
	}
}
