package impl

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/metrics"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

type RecoverableRedisOption func(*RecoverableRedisStreamClient) error

// ClusterMode selects how the client subscribes to keyspace notifications and recovers work.
type ClusterMode int

const (
	// ClusterModeSingleShard is the default. Keyspace notifications are subscribed to on the
	// single (logical) shard the client is connected to. This is the historical behavior and is
	// appropriate for a single Redis node, primary/replica, or Sentinel deployment.
	ClusterModeSingleShard ClusterMode = iota
	// ClusterModeOSS targets an OSS Redis Cluster. Keyspace notifications fire only on the shard
	// owning the expiring key, so the client subscribes on every master node and relies on the
	// periodic reconciliation scan as the authoritative, cluster-safe recovery mechanism. Requires
	// the underlying client to be a *redis.ClusterClient.
	ClusterModeOSS
)

// RecoveryConfig holds configuration for the periodic reconciliation scan that recovers
// pending LBS messages whose owning consumer has died (cluster-safe XACK + XADD re-queue).
type RecoveryConfig struct {
	// ReconciliationInterval is the base period of the periodic recovery scan. Jitter is added
	// on top of this to avoid synchronized scans ("thundering herd") across consumers.
	ReconciliationInterval time.Duration

	// MinIdleTime is the minimum time a pending message must have been idle before it is
	// eligible for recovery. Should be comfortably larger than the heartbeat interval.
	MinIdleTime time.Duration

	// BatchSize is the maximum number of pending messages inspected per scan.
	BatchSize int

	// MaxRetries is the maximum number of times a message may be re-queued before it is routed
	// to the DLQ (or dropped if no DLQ is configured). This is distinct from RetryConfig.MaxRetries
	// which governs LBS read retries.
	MaxRetries int

	// DLQStream, if non-empty, is the name of the stream to which messages exceeding MaxRetries
	// are routed. If empty, such messages are acknowledged and dropped.
	DLQStream string
}

// DefaultRecoveryConfig returns the default recovery configuration.
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		ReconciliationInterval: configs.DefaultReconciliationInterval,
		MinIdleTime:            configs.DefaultMinIdleTime,
		BatchSize:              configs.DefaultReconciliationBatchSize,
		MaxRetries:             configs.DefaultMaxReQueueRetries,
		DLQStream:              "",
	}
}

// Validate checks if the recovery configuration is valid.
func (rc RecoveryConfig) Validate() error {
	if rc.ReconciliationInterval <= 0 {
		return fmt.Errorf("%w: reconciliationInterval must be greater than 0", errs.ErrInvalidRecoveryConfig)
	}
	if rc.MinIdleTime <= 0 {
		return fmt.Errorf("%w: minIdleTime must be greater than 0", errs.ErrInvalidRecoveryConfig)
	}
	if rc.BatchSize <= 0 {
		return fmt.Errorf("%w: batchSize must be greater than 0", errs.ErrInvalidRecoveryConfig)
	}
	if rc.MaxRetries < 0 {
		return fmt.Errorf("%w: maxRetries must be >= 0", errs.ErrInvalidRecoveryConfig)
	}
	return nil
}

// RetryConfig holds all retry-related configuration
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts
	// -1 => unlimited retries
	// 0 => no retries, fail immediately
	// >0 => specific number of retry attempts
	MaxRetries int

	// InitialRetryDelay is the initial delay before the first retry attempt
	InitialRetryDelay time.Duration

	// MaxRetryDelay is the maximum delay between retries (exponential backoff cap)
	MaxRetryDelay time.Duration
}

// DefaultRetryConfig returns the default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:        configs.DefaultMaxRetries,
		InitialRetryDelay: configs.DefaultInitialRetryDelay,
		MaxRetryDelay:     configs.DefaultMaxRetryDelay,
	}
}

// Validate checks if the retry configuration is valid
func (rc RetryConfig) Validate() error {
	if rc.MaxRetries < -1 {
		return fmt.Errorf("maxRetries must be -1 (unlimited) or >= 0")
	}
	if rc.InitialRetryDelay <= 0 {
		return fmt.Errorf("initialRetryDelay must be greater than 0")
	}
	if rc.MaxRetryDelay <= 0 {
		return fmt.Errorf("maxRetryDelay must be greater than 0")
	}
	if rc.InitialRetryDelay > rc.MaxRetryDelay {
		return fmt.Errorf("initialRetryDelay cannot be greater than maxRetryDelay")
	}
	return nil
}

// WithLBSIdleTime sets the time after which a message is considered idle and will be recovered
func WithLBSIdleTime(idleTime time.Duration) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		// idleTime must be greater than 2 * heartbeat interval at least
		if idleTime == 0 || idleTime < (2*r.hbInterval) {
			return errs.ErrInvalidIdleTime
		}

		r.lbsIdleTime = idleTime
		return nil
	}
}

// WithLBSRecoveryCount sets the number of messages to fetch at a time during recovery
func WithLBSRecoveryCount(count int) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if count <= 0 {
			return errs.ErrInvalidRecoveryCount
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
			return errs.ErrInvalidKspChanSize
		}

		r.kspChanSize = size
		return nil
	}
}

// WithKspChanTimeout is the duration after which an outstanding pub sub message
// from redis pub sub is dropped from channel
func WithKspChanTimeout(timeout time.Duration) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if timeout < time.Minute {
			return errs.ErrInvalidKspChanTimeout
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
			return errs.ErrInvalidOutputChanSize
		}

		r.outputChan = make(chan notifs.RecoverableRedisNotification, size)
		return nil
	}
}

// WithRetryConfig configures retry-related settings
func WithRetryConfig(config RetryConfig) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if err := config.Validate(); err != nil {
			return err
		}

		r.maxRetries = config.MaxRetries
		r.initialRetryDelay = config.InitialRetryDelay
		r.maxRetryDelay = config.MaxRetryDelay
		return nil
	}
}

// WithLogger allows clients to provide their own logger implementation based on slog.Logger
func WithLogger(logger *slog.Logger) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		r.logger = logger
		return nil
	}
}

// WithMetricsRecorder allows clients to provide their own metrics recorder
// implementation based on MetricsRecorder interface
func WithMetricsRecorder(recorder metrics.Recorder) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		r.metricsRecorder = recorder
		return nil
	}
}

// WithRecoveryConfig configures the periodic reconciliation scan used to recover pending
// LBS messages whose owning consumer has died.
func WithRecoveryConfig(config RecoveryConfig) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		if err := config.Validate(); err != nil {
			return err
		}

		r.recoveryConfig = config
		return nil
	}
}

// WithClusterMode selects the keyspace-notification and recovery strategy. Use ClusterModeOSS
// when running against an OSS Redis Cluster; this requires the underlying client to be a
// *redis.ClusterClient (validated at Init).
func WithClusterMode(mode ClusterMode) RecoverableRedisOption {
	return func(r *RecoverableRedisStreamClient) error {
		r.clusterMode = mode
		return nil
	}
}
