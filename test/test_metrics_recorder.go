package test

import (
	"sync"
	"time"
)

type testMetricsRecorder struct {
	mu sync.Mutex

	startupRecoveryCount   int
	claimCount             int
	lockAcquisitionCount   int
	lockExtensionCount     int
	lockReleaseCount       int
	kspNotificationCount   int
	kspNotificationDropped int
	streamProcessingStart  map[string]time.Time
	streamProcessingEnd    map[string]time.Time
}

func (t *testMetricsRecorder) RecordStartupRecovery(success bool, unackedCount int, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.startupRecoveryCount++
}

func (t *testMetricsRecorder) RecordClaimAttempt(streamName string, success bool, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.claimCount++
}

func (t *testMetricsRecorder) RecordLockAcquisitionAttempt(streamName string, success bool, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lockAcquisitionCount++
}

func (t *testMetricsRecorder) RecordLockExtensionAttempt(streamName string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lockExtensionCount++
}

func (t *testMetricsRecorder) RecordLockReleaseAttempt(streamName string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lockReleaseCount++
}

func (t *testMetricsRecorder) RecordStreamProcessingStart(streamName string, start time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.streamProcessingStart == nil {
		t.streamProcessingStart = make(map[string]time.Time)
	}
	t.streamProcessingStart[streamName] = start
}

func (t *testMetricsRecorder) RecordStreamProcessingEnd(streamName string, end time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.streamProcessingEnd == nil {
		t.streamProcessingEnd = make(map[string]time.Time)
	}
	t.streamProcessingEnd[streamName] = end
}

func (t *testMetricsRecorder) RecordKspNotification(streamName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kspNotificationCount++
}

func (t *testMetricsRecorder) RecordKspNotificationDropped() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kspNotificationDropped++
}

// Getter methods to support assertions in tests
func (t *testMetricsRecorder) StartupRecoveryCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startupRecoveryCount
}

func (t *testMetricsRecorder) StartupRecoveryFailures() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startupRecoveryCount
}

func (t *testMetricsRecorder) ClaimCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.claimCount
}

func (t *testMetricsRecorder) LockAcquisitionCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lockAcquisitionCount
}

func (t *testMetricsRecorder) LockExtensionCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lockExtensionCount
}

func (t *testMetricsRecorder) LockReleaseCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lockReleaseCount
}

func (t *testMetricsRecorder) KspNotificationCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.kspNotificationCount
}

func (t *testMetricsRecorder) KspNotificationDropped() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.kspNotificationDropped
}

func (t *testMetricsRecorder) StreamProcessingStartCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.streamProcessingStart)
}

func (t *testMetricsRecorder) StreamProcessingEndCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.streamProcessingEnd)
}
