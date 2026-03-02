package metrics

import "time"

// MetricsRecorder defines the interface for recording metrics related to Redis mutex operations and stream processing.
type Recorder interface {
	// RecordStartupRecovery records the outcome of the startup recovery process, including whether it was successful,
	// the number of unacked messages, and the duration of the recovery.
	RecordStartupRecovery(success bool, unackedCount int, duration time.Duration)
	// RecordClaimAttempt records an attempt to claim a mutex, including whether it was successful and how long it took.
	RecordClaimAttempt(streamName string, success bool, duration time.Duration)
	// RecordRecoveryAttempt records an attempt to recover a lock, including whether it was successful and how long it took.
	RecordLockAcquisitionAttempt(streamName string, success bool, duration time.Duration)
	// RecordLockExtension records an attempt to extend a lock, including whether it was successful.
	RecordLockExtensionAttempt(streamName string, success bool)
	// RecordLockRelease records an attempt to release a lock, including whether it was successful.
	RecordLockReleaseAttempt(streamName string, success bool)
	// RecordStreamProcessingStart records the start of stream processing for a given stream.
	RecordStreamProcessingStart(streamName string, startTime time.Time)
	// RecordStreamProcessingEnd records the end of stream processing and the total duration.
	RecordStreamProcessingEnd(streamName string, startTime time.Time)
	// RecordKspNotification records the receipt of a keyspace notification for a stream.
	RecordKspNotification(streamName string)
	// RecordKspNotificationDropped records the event of a keyspace notification being dropped due to a full broker channel.
	RecordKspNotificationDropped()
}
