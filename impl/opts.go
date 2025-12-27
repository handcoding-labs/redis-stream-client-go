package impl

import (
	"fmt"
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/notifs"
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

// WithKspChanSize sets the size of the ksp channel which corresponds to number of
// pub sub notifications that we can receive from redis
func WithKspChanSize(size int) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if size <= 0 {
			return fmt.Errorf("kspChanSize must be a positive number")
		}

		r.kspChanSize = size
		return nil
	}
}

// WithKspChanTimeout is the duration after which an outstanding pub sub message
// from redis pub sub is dropped from channel
func WithKspChanTimeout(timeout time.Duration) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if timeout == 0 {
			return fmt.Errorf("timeout cannot be zero value")
		}

		r.kspChanTimeout = timeout
		return nil
	}
}

// WithForceConfigOverride when set overrides the redis configuration for
// key space notifications
func WithForceConfigOverride() RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		r.forceOverrideConfig = true
		return nil
	}
}

// WithOutputChanSize lets the clients set the outputChanSize where different
// notifications are sent
func WithOutputChanSize(size int) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if size <= 0 {
			return fmt.Errorf("outputChan size must be a positive number")
		}

		r.outputChan = make(chan notifs.RecoverableRedisNotification, size)
		return nil
	}
}
