package metrics

import "time"

// MetricsRecorder defines the interface for recording metrics related to Redis mutex operations and stream processing.
type Recorder interface {
	// RecordStartupRecovery records the outcome of the startup recovery process, including whether it was successful,
	// the number of unacked messages, and the duration of the recovery.
	RecordStartupRecovery(success bool, unackedCount int, duration time.Duration)
	// RecordClaimAttempt records an attempt to claim a mutex, including whether it was successful and how long it took.
	RecordClaimAttempt(streamName string, success bool, duration time.Duration)
	// RecordLockAcquisitionAttempt records an attempt to acquire or recover a lock (either during
	// startup recovery or when claiming an expired stream), including whether it was successful
	// and how long it took.
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
	// RecordKspNotificationDropped records the event of a keyspace notification being dropped
	// due to a full broker channel.
	RecordKspNotificationDropped()
	// RecordReconciliationScan records the outcome of a single periodic reconciliation scan,
	// including how many messages were re-queued, skipped because their lock was still held by a
	// live consumer, routed to the DLQ, and the total scan duration.
	RecordReconciliationScan(requeued, skippedAlive, dlqRouted int, duration time.Duration)
	// RecordReQueue records an attempt to re-queue a pending message (XACK + XADD), including
	// whether this consumer won the XACK race and completed the re-add.
	RecordReQueue(streamName string, success bool)
	// RecordDLQRouting records that a message exceeded MaxRetries and was routed to the DLQ.
	RecordDLQRouting(streamName string)
	// RecordMutexAliveSkip records that a pending message was skipped during recovery because its
	// lock key still exists (the owning consumer is alive but slow).
	RecordMutexAliveSkip(streamName string)
	// RecordAckAddGap records the rare case where XACK succeeded but the subsequent XADD failed,
	// leaving the message dropped from the PEL without being re-queued.
	RecordAckAddGap(streamName string)
	// RecordTopologyReset records an attempt to reset the cluster topology and re-subscribe to
	// keyspace notifications (ClusterModeOSS only).
	RecordTopologyReset(success bool)
	// RecordMasterKeyspaceSetup records the per-master outcome of enabling keyspace notifications
	// across the cluster (ClusterModeOSS only). It makes partial failures — where some masters are
	// configured and others are not — observable.
	RecordMasterKeyspaceSetup(success bool)
}
