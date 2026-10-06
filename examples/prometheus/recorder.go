// Package prometheus provides a reference MetricsRecorder implementation
// using Prometheus. Copy this file into your own codebase and adjust as needed.
//
// **Important:** this example must be updated whenever the
// `metrics.Recorder` interface changes.  The method names used
// here match the interface defined in `metrics/recorder.go`.
package prometheusmetric

import (
	"strconv"
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
	reconciliationScanTotal         prometheus.Counter
	reconciliationScanDuration      prometheus.Histogram
	reconciliationRequeuedTotal     prometheus.Counter
	reconciliationSkippedAliveTotal prometheus.Counter
	reconciliationDLQRoutedTotal    prometheus.Counter
	requeueTotal                    *prometheus.CounterVec
	dlqRoutingTotal                 *prometheus.CounterVec
	mutexAliveSkipTotal             *prometheus.CounterVec
	ackAddGapTotal                  *prometheus.CounterVec
	topologyResetTotal              *prometheus.CounterVec
	masterKeyspaceSetupTotal        *prometheus.CounterVec

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

		reconciliationScanTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "redis_reconciliation_scan_total",
			Help: "Total number of periodic reconciliation scans performed.",
		}),

		reconciliationScanDuration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "redis_reconciliation_scan_duration_seconds",
			Help:    "Duration of a reconciliation scan in seconds.",
			Buckets: prometheus.DefBuckets,
		}),

		reconciliationRequeuedTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "redis_reconciliation_requeued_total",
			Help: "Total number of messages re-queued by reconciliation scans.",
		}),

		reconciliationSkippedAliveTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "redis_reconciliation_skipped_alive_total",
			Help: "Total number of messages skipped because their lock was still held by a live consumer.",
		}),

		reconciliationDLQRoutedTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "redis_reconciliation_dlq_routed_total",
			Help: "Total number of messages routed to the DLQ by reconciliation scans.",
		}),

		requeueTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_requeue_total",
			Help: "Total number of re-queue attempts, labeled by stream and success.",
		}, []string{"stream", "success"}),

		dlqRoutingTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_dlq_routing_total",
			Help: "Total number of messages routed to the DLQ, labeled by stream.",
		}, []string{"stream"}),

		mutexAliveSkipTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_mutex_alive_skip_total",
			Help: "Total number of recovery skips because the lock key still exists, labeled by stream.",
		}, []string{"stream"}),

		ackAddGapTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_ack_add_gap_total",
			Help: "Total number of XACK-succeeded-but-XADD-failed events, labeled by stream.",
		}, []string{"stream"}),

		topologyResetTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_topology_reset_total",
			Help: "Total number of cluster topology resets, labeled by success.",
		}, []string{"success"}),

		masterKeyspaceSetupTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "redis_master_keyspace_setup_total",
			Help: "Total number of per-master keyspace-notification setup attempts (ClusterModeOSS), labeled by success.",
		}, []string{"success"}),
	}
}

func (p *PrometheusRecorder) RecordClaimAttempt(streamName string, success bool, duration time.Duration) {
	p.claimTotal.WithLabelValues(streamName, strconv.FormatBool(success)).Inc()
	p.claimDurationSeconds.WithLabelValues(streamName).Observe(duration.Seconds())
}

func (p *PrometheusRecorder) RecordLockAcquisitionAttempt(streamName string, success bool, duration time.Duration) {
	p.startupRecoveryTotal.WithLabelValues(strconv.FormatBool(success)).Inc()
	p.startupRecoveryDurationSeconds.WithLabelValues(streamName).Observe(duration.Seconds())
}

func (p *PrometheusRecorder) RecordLockExtensionAttempt(streamName string, success bool) {
	p.lockExtensionTotal.WithLabelValues(streamName, strconv.FormatBool(success)).Inc()
}

func (p *PrometheusRecorder) RecordLockReleaseAttempt(streamName string, success bool) {
	p.lockReleaseTotal.WithLabelValues(streamName, strconv.FormatBool(success)).Inc()
}

// RecordStartupRecovery implements the interface method for startup recovery.
func (p *PrometheusRecorder) RecordStartupRecovery(success bool, unackedCount int, duration time.Duration) {
	p.startupRecoveryTotal.WithLabelValues(strconv.FormatBool(success)).Inc()
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

func (p *PrometheusRecorder) RecordReconciliationScan(requeued, skippedAlive, dlqRouted int, duration time.Duration) {
	p.reconciliationScanTotal.Inc()
	p.reconciliationScanDuration.Observe(duration.Seconds())
	p.reconciliationRequeuedTotal.Add(float64(requeued))
	p.reconciliationSkippedAliveTotal.Add(float64(skippedAlive))
	p.reconciliationDLQRoutedTotal.Add(float64(dlqRouted))
}

func (p *PrometheusRecorder) RecordReQueue(streamName string, success bool) {
	p.requeueTotal.WithLabelValues(streamName, strconv.FormatBool(success)).Inc()
}

func (p *PrometheusRecorder) RecordDLQRouting(streamName string) {
	p.dlqRoutingTotal.WithLabelValues(streamName).Inc()
}

func (p *PrometheusRecorder) RecordMutexAliveSkip(streamName string) {
	p.mutexAliveSkipTotal.WithLabelValues(streamName).Inc()
}

func (p *PrometheusRecorder) RecordAckAddGap(streamName string) {
	p.ackAddGapTotal.WithLabelValues(streamName).Inc()
}

func (p *PrometheusRecorder) RecordTopologyReset(success bool) {
	p.topologyResetTotal.WithLabelValues(strconv.FormatBool(success)).Inc()
}

func (p *PrometheusRecorder) RecordMasterKeyspaceSetup(success bool) {
	p.masterKeyspaceSetupTotal.WithLabelValues(strconv.FormatBool(success)).Inc()
}
