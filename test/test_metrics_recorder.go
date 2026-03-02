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

	orphanClaimFailures     int
	lockAcquisitionFailures int
	lockExtensionFailures   int
	lockReleaseFailures     int
	startupRecoveryFailures int
	streamProcessingStart   map[string]time.Time
	streamProcessingEnd     map[string]time.Time
}

func (t *testMetricsRecorder) RecordStartupRecovery(success bool, unackedCount int, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.startupRecoveryCount++
	if !success {
		t.startupRecoveryFailures++
	}
}

func (t *testMetricsRecorder) RecordClaimAttempt(streamName string, success bool, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.claimCount++
	if !success {
		t.claimCount++
	}
}

func (t *testMetricsRecorder) RecordLockAcquisitionAttempt(streamName string, success bool, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lockAcquisitionCount++
	if !success {
		t.lockAcquisitionFailures++
	}
}

func (t *testMetricsRecorder) RecordLockExtensionAttempt(streamName string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lockExtensionCount++
	if !success {
		t.lockExtensionFailures++
	}
}

func (t *testMetricsRecorder) RecordLockReleaseAttempt(streamName string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lockReleaseCount++
	if !success {
		t.lockReleaseFailures++
	}
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
