package impl

import (
	"fmt"
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// RecoveryConfig holds configuration for the periodic reconciliation scan that recovers
// pending LBS messages whose owning consumer has died (cluster-safe XACK + XADD re-queue).
//
// Prefer NewRecoveryConfigBuilder for construction: it starts from the defaults, lets you override
// only what you need, and validates on Build.
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
	// to the DLQ. This is distinct from RetryConfig.MaxRetries which governs LBS read retries.
	MaxRetries int

	// DLQStream is the name of the stream to which messages exceeding MaxRetries are routed. When
	// left empty it defaults to "<lbs-name>-dlq" (see NewRedisStreamClient) so poison messages are
	// preserved rather than dropped.
	DLQStream string
}

// DefaultRecoveryConfig returns the default recovery configuration. DLQStream is left empty here so
// that NewRedisStreamClient can derive a per-service default once the service name is known.
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

// RecoveryConfigBuilder builds a RecoveryConfig with a fluent API, starting from the defaults and
// validating on Build. Example:
//
//	cfg, err := NewRecoveryConfigBuilder().
//		WithReconciliationInterval(30 * time.Second).
//		WithDLQStream("orders-dlq").
//		Build()
type RecoveryConfigBuilder struct {
	cfg RecoveryConfig
}

// NewRecoveryConfigBuilder returns a builder seeded with DefaultRecoveryConfig.
func NewRecoveryConfigBuilder() *RecoveryConfigBuilder {
	return &RecoveryConfigBuilder{cfg: DefaultRecoveryConfig()}
}

// WithReconciliationInterval sets the base period of the periodic recovery scan.
func (b *RecoveryConfigBuilder) WithReconciliationInterval(d time.Duration) *RecoveryConfigBuilder {
	b.cfg.ReconciliationInterval = d
	return b
}

// WithMinIdleTime sets the minimum idle time before a pending message is eligible for recovery.
func (b *RecoveryConfigBuilder) WithMinIdleTime(d time.Duration) *RecoveryConfigBuilder {
	b.cfg.MinIdleTime = d
	return b
}

// WithBatchSize sets the maximum number of pending messages inspected per scan.
func (b *RecoveryConfigBuilder) WithBatchSize(n int) *RecoveryConfigBuilder {
	b.cfg.BatchSize = n
	return b
}

// WithMaxRetries sets the number of re-queue attempts before a message is routed to the DLQ.
func (b *RecoveryConfigBuilder) WithMaxRetries(n int) *RecoveryConfigBuilder {
	b.cfg.MaxRetries = n
	return b
}

// WithDLQStream sets the DLQ stream name. Leave unset to use the per-service default.
func (b *RecoveryConfigBuilder) WithDLQStream(name string) *RecoveryConfigBuilder {
	b.cfg.DLQStream = name
	return b
}

// Build validates the accumulated configuration and returns it.
func (b *RecoveryConfigBuilder) Build() (RecoveryConfig, error) {
	if err := b.cfg.Validate(); err != nil {
		return RecoveryConfig{}, err
	}
	return b.cfg, nil
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
