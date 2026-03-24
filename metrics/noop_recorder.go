package metrics

import "time"

// NoopRecorder is a MetricsRecorder implementation that does nothing.
// This can be used as a default recorder when metrics recording is not needed.
type NoopRecorder struct{}

func (n *NoopRecorder) RecordStartupRecovery(success bool, unackedCount int, duration time.Duration) {
}
func (n *NoopRecorder) RecordClaimAttempt(streamName string, success bool, duration time.Duration) {}
func (n *NoopRecorder) RecordLockAcquisitionAttempt(streamName string, success bool, duration time.Duration) {
}
func (n *NoopRecorder) RecordLockExtensionAttempt(streamName string, success bool)         {}
func (n *NoopRecorder) RecordLockReleaseAttempt(streamName string, success bool)           {}
func (n *NoopRecorder) RecordStreamProcessingStart(streamName string, startTime time.Time) {}
func (n *NoopRecorder) RecordStreamProcessingEnd(streamName string, startTime time.Time)   {}
func (n *NoopRecorder) RecordKspNotification(streamName string)                            {}
func (n *NoopRecorder) RecordKspNotificationDropped()                                      {}
