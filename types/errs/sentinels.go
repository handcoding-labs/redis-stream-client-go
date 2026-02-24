package errs

import "errors"

// Config validation
var (
	ErrInvalidIdleTime       = errors.New("invalid heartbeat interval value: must be >0 and <2*heartbeat interval")
	ErrInvalidRecoveryCount  = errors.New("invalid recovery count: must be non-zero positive integer")
	ErrInvalidKspChanSize    = errors.New("invalid ksp chan size: must be non-zero positive integer")
	ErrInvalidKspChanTimeout = errors.New("invalid ksp chan timeout: must be >= 1 min")
	ErrInvalidOutputChanSize = errors.New("invalid output chan size: must be non-zero positive integer")
	ErrPodConfigMissing      = errors.New("pod name or pod IP is missing")
)

// Input validation
var (
	ErrInvalidKeyForLBSMessage  = errors.New("invalid key for LBS; must be `lbs-input`")
	ErrInvalidLBSMessage        = errors.New("invalid or malformed LBS message")
	ErrNoDatastreamInLBSMessage = errors.New("lbs message does not have data stream name")
)

// Internal state
var (
	ErrAlreadyClaimed                = errors.New("already claimed")
	ErrDataStreamNotFound            = errors.New("data stream not found")
	ErrExistingConfigWithoutOverride = errors.New("existing configuration detected without force override option")
)
