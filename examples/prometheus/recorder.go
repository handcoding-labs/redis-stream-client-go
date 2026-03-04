// Package prometheus provides a reference MetricsRecorder implementation
// using Prometheus. Copy this file into your own codebase and adjust as needed.
//
// **Important:** this example must be updated whenever the
// `metrics.Recorder` interface changes.  The method names used
// here match the interface defined in `metrics/recorder.go`.
package prometheusmetric

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PrometheusRecorder is a MetricsRecorder implementation using Prometheus.
type PrometheusRecorder struct {
	claimTotal                      *prometheus.CounterVec
	claimDurationSeconds            *prometheus.HistogramVec
	lockExtensionTotal              *prometheus.CounterVec
	lockReleaseTotal                *prometheus.CounterVec
	startupRecoveryTotal            *prometheus.CounterVec
	startupRecoveryDurationSeconds  *prometheus.HistogramVec
	streamProcessingDurationSeconds *prometheus.HistogramVec
	kspNotificationTotal            *prometheus.CounterVec
	kspNotificationDroppedTotal     prometheus.Counter

	// internal state
	streamStarts map[string]time.Time
}

// NewPrometheusRecorder creates a new PrometheusRecorder and registers
// all metrics with the provided Prometheus registerer.
func NewPrometheusRecorder(reg prometheus.Registerer) *PrometheusRecorder {
	factory := promauto.With(reg)

	return &PrometheusRecorder{
		claimTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_mutex_claim_total",
			Help: "Total number of mutex claim attempts, labeled by stream and success.",
		}, []string{"stream", "success"}),

		claimDurationSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "redis_mutex_claim_duration_seconds",
			Help:    "Duration of mutex claim attempts in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"stream"}),

		lockExtensionTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_mutex_lock_extension_total",
			Help: "Total number of lock extension attempts, labeled by stream and success.",
		}, []string{"stream", "success"}),

		lockReleaseTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_mutex_lock_release_total",
			Help: "Total number of lock release attempts, labeled by stream and success.",
		}, []string{"stream", "success"}),

		startupRecoveryTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_mutex_startup_recovery_total",
			Help: "Total number of startup recovery attempts, labeled by success.",
		}, []string{"success"}),

		startupRecoveryDurationSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "redis_mutex_startup_recovery_duration_seconds",
			Help:    "Duration of startup recovery in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"stream"}),

		streamProcessingDurationSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "redis_mutex_stream_processing_duration_seconds",
			Help:    "Duration of stream processing in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"stream"}),

		kspNotificationTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_mutex_ksp_notification_total",
			Help: "Total number of keyspace notifications received, labeled by stream.",
		}, []string{"stream"}),

		kspNotificationDroppedTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "redis_mutex_ksp_notification_dropped_total",
			Help: "Total number of keyspace notifications dropped due to full broker channel.",
		}),
	}
}

func (p *PrometheusRecorder) RecordClaimAttempt(streamName string, success bool, duration time.Duration) {
	p.claimTotal.WithLabelValues(streamName, boolToString(success)).Inc()
	p.claimDurationSeconds.WithLabelValues(streamName).Observe(duration.Seconds())
}

func (p *PrometheusRecorder) RecordLockAcquisitionAttempt(streamName string, success bool, duration time.Duration) {
	p.startupRecoveryTotal.WithLabelValues(boolToString(success)).Inc()
	p.startupRecoveryDurationSeconds.WithLabelValues(streamName).Observe(duration.Seconds())
}

func (p *PrometheusRecorder) RecordLockExtensionAttempt(streamName string, success bool) {
	p.lockExtensionTotal.WithLabelValues(streamName, boolToString(success)).Inc()
}

func (p *PrometheusRecorder) RecordLockReleaseAttempt(streamName string, success bool) {
	p.lockReleaseTotal.WithLabelValues(streamName, boolToString(success)).Inc()
}

// RecordStartupRecovery implements the interface method for startup recovery.
func (p *PrometheusRecorder) RecordStartupRecovery(success bool, unackedCount int, duration time.Duration) {
	p.startupRecoveryTotal.WithLabelValues(boolToString(success)).Inc()
	// also record duration (unackedCount not stored here)
	p.startupRecoveryDurationSeconds.WithLabelValues("").Observe(duration.Seconds())
}

func (p *PrometheusRecorder) RecordStreamProcessingStart(streamName string, start time.Time) {
	if p.streamStarts == nil {
		p.streamStarts = make(map[string]time.Time)
	}
	p.streamStarts[streamName] = start
}

func (p *PrometheusRecorder) RecordStreamProcessingEnd(streamName string, end time.Time) {
	start, ok := p.streamStarts[streamName]
	if ok {
		duration := end.Sub(start)
		p.streamProcessingDurationSeconds.WithLabelValues(streamName).Observe(duration.Seconds())
		delete(p.streamStarts, streamName)
	}
}

func (p *PrometheusRecorder) RecordKspNotification(streamName string) {
	p.kspNotificationTotal.WithLabelValues(streamName).Inc()
}

func (p *PrometheusRecorder) RecordKspNotificationDropped() {
	p.kspNotificationDroppedTotal.Inc()
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
