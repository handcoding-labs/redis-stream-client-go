package impl

import (
	"context"
	"math/rand"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// unknownDataStream labels metrics and logs for entries whose data stream name could not be parsed
// (poison messages routed straight to the DLQ).
const unknownDataStream = "unknown"

// reQueueOutcome describes what happened when a pending LBS message was processed for recovery.
type reQueueOutcome int

const (
	// reQueueError indicates an unexpected error occurred while attempting recovery; the returned
	// error carries the detail and the caller should log/observe it. It is the zero value so an
	// early error return naturally reports this outcome rather than masquerading as a lost race.
	reQueueError reQueueOutcome = iota
	// reQueueLostRace is a benign, expected outcome under concurrency: the pending entry was already
	// handled by another recoverer, or it no longer exists in the stream. This arises in the window
	// between XPENDING listing an ID and this caller's XRANGE/XACK — a peer recoverer may XACK+XADD
	// it, or DoneStream may XDEL it. It also covers unparseable poison entries that are acked and
	// dropped. Nothing for this caller to do.
	reQueueLostRace
	// reQueueSkippedAlive means the lock key still exists, so the owning consumer is alive (slow)
	// and the message was intentionally left in place. This is the duplicate-processing fix.
	reQueueSkippedAlive
	// reQueueRequeued means this caller won the XACK race and re-added the task to the LBS.
	reQueueRequeued
	// reQueueDLQ means the message exceeded MaxRetries and was routed to the DLQ (or dropped if no
	// DLQ is configured).
	reQueueDLQ
)

// reQueue implements the cluster-safe recovery primitive shared by the periodic reconciliation
// scan and Claim. It reads the pending LBS entry identified by idInLBS, verifies the owning
// consumer is actually dead (lock key absent), then either re-adds the task as a new message
// (XACK + XADD) or routes it to the DLQ when retries are exhausted.
//
// The XACK-first ordering de-duplicates concurrent recoverers: only the consumer whose XACK removes
// the entry from the PEL proceeds to XADD; the others observe acked==0 and back off.
func (r *RecoverableRedisStreamClient) reQueue(ctx context.Context, idInLBS string) (reQueueOutcome, string, error) {
	entry, exists, err := r.readLBSEntry(ctx, idInLBS)
	if err != nil {
		return reQueueError, "", err
	}
	if !exists {
		// the entry no longer exists in the stream; clear any lingering PEL ownership and bail
		if ackErr := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), idInLBS).Err(); ackErr != nil {
			r.logger.Warn("error acking already-removed LBS entry", "error", ackErr, "id_in_lbs", idInLBS)
		}
		return reQueueLostRace, "", nil
	}

	dataStreamName, err := entry.dataStreamName()
	if err != nil {
		// The entry can't be parsed into a valid LBS message, so it can never be processed. Route
		// it to the DLQ for inspection rather than silently dropping it: DLQ routing only needs the
		// configured DLQ stream and the raw payload, not the (unknown) data stream name. The
		// mutex-liveness and retry checks don't apply — a poison message stays poison for everyone.
		r.logger.Warn("unparseable LBS entry during re-queue; routing to DLQ",
			"error", err, "id_in_lbs", idInLBS)
		out, dlqErr := r.routeToDLQ(ctx, unknownDataStream, entry, entry.retryCount(), configs.DLQReasonUnparseable)
		return out, unknownDataStream, dlqErr
	}

	lbsInfo, err := notifs.CreateByParts(dataStreamName, idInLBS)
	if err != nil {
		return reQueueError, dataStreamName, errs.NewRedisError(errs.OpReQueue, err)
	}

	// Mutex-liveness check: if the lock still exists, the owning consumer is alive (just slow).
	// Skipping here is what prevents duplicate processing of slow-but-alive consumers.
	alive, err := r.ownerAlive(ctx, lbsInfo)
	if err != nil {
		return reQueueError, dataStreamName, err
	}
	if alive {
		r.metricsRecorder.RecordMutexAliveSkip(dataStreamName)
		r.logger.Debug("skipping re-queue; lock still held by live consumer",
			"data_stream", dataStreamName, "mutex_key", lbsInfo.FormMutexKey())
		return reQueueSkippedAlive, dataStreamName, nil
	}

	next := entry.retryCount() + 1

	// Retries exhausted -> DLQ.
	if next > r.recoveryConfig.MaxRetries {
		out, dlqErr := r.routeToDLQ(ctx, dataStreamName, entry, next, configs.DLQReasonMaxRetries)
		return out, dataStreamName, dlqErr
	}

	// XACK-first dedup across concurrent recoverers.
	acked, err := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), idInLBS).Result()
	if err != nil {
		r.metricsRecorder.RecordReQueue(dataStreamName, false)
		return reQueueError, dataStreamName, errs.NewRedisError(errs.OpReQueue, err)
	}
	if acked == 0 {
		// someone else already acked (and presumably re-queued) this message
		return reQueueLostRace, dataStreamName, nil
	}

	// Re-add the task as a brand-new message so XREADGROUP redistributes it normally.
	newValues := entry.cloneValues()
	newValues[configs.RetryCountField] = strconv.Itoa(next)
	if err := r.redisClient.XAdd(ctx, &redis.XAddArgs{
		Stream: r.lbsName(),
		Values: newValues,
	}).Err(); err != nil {
		// Atomicity gap (RFC-accepted): XACK succeeded but XADD failed. The message is now lost
		// from the PEL without being re-added. Surface it loudly and via a metric.
		r.metricsRecorder.RecordAckAddGap(dataStreamName)
		r.metricsRecorder.RecordReQueue(dataStreamName, false)
		r.logger.Error("XADD failed after XACK; message dropped from PEL (atomicity gap)",
			"error", err, "data_stream", dataStreamName, "id_in_lbs", idInLBS)
		return reQueueError, dataStreamName, errs.NewRedisError(errs.OpReQueue, err)
	}

	// Remove the now-acked original entry from the stream.
	if err := r.redisClient.XDel(ctx, r.lbsName(), idInLBS).Err(); err != nil {
		r.logger.Warn("error deleting original LBS entry after re-queue",
			"error", err, "id_in_lbs", idInLBS)
	}

	r.metricsRecorder.RecordReQueue(dataStreamName, true)
	r.logger.Info("re-queued LBS message for redistribution",
		"data_stream", dataStreamName, "retry_count", next)
	return reQueueRequeued, dataStreamName, nil
}

// routeToDLQ acknowledges a message and re-adds it to the configured DLQ stream, tagging it with the
// supplied reason and retry count. It drops the message only when no DLQ is configured. Like reQueue
// it is XACK-first for cross-recoverer dedup. dataStreamName is used only for metric labels and log
// context — the DLQ target and payload come from recoveryConfig.DLQStream and the entry itself — so
// callers without a parseable stream name may pass a placeholder (see unknownDataStream).
func (r *RecoverableRedisStreamClient) routeToDLQ(
	ctx context.Context,
	dataStreamName string,
	entry lbsEntry,
	retryCount int,
	reason string,
) (reQueueOutcome, error) {
	acked, err := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), entry.id).Result()
	if err != nil {
		return reQueueError, errs.NewRedisError(errs.OpRouteDLQ, err)
	}
	if acked == 0 {
		return reQueueLostRace, nil
	}

	if r.recoveryConfig.DLQStream != "" {
		dlqValues := entry.cloneValues()
		dlqValues[configs.RetryCountField] = strconv.Itoa(retryCount)
		dlqValues[configs.DLQReasonField] = reason
		if err := r.redisClient.XAdd(ctx, &redis.XAddArgs{
			Stream: r.recoveryConfig.DLQStream,
			// Approximate MAXLEN trim keeps the DLQ from growing unbounded (nothing acks it).
			// MaxLen == 0 leaves the stream uncapped.
			MaxLen: r.recoveryConfig.DLQMaxLen,
			Approx: true,
			Values: dlqValues,
		}).Err(); err != nil {
			r.metricsRecorder.RecordAckAddGap(dataStreamName)
			r.logger.Error("XADD to DLQ failed after XACK; message dropped (atomicity gap)",
				"error", err, "data_stream", dataStreamName, "dlq", r.recoveryConfig.DLQStream)
			return reQueueDLQ, errs.NewRedisError(errs.OpRouteDLQ, err)
		}
		r.metricsRecorder.RecordDLQRouting(dataStreamName)
		r.logger.Warn("routed message to DLQ",
			"data_stream", dataStreamName, "retry_count", retryCount,
			"reason", reason, "dlq", r.recoveryConfig.DLQStream)
	} else {
		r.logger.Warn("dropping message (no DLQ configured)",
			"data_stream", dataStreamName, "retry_count", retryCount, "reason", reason)
	}

	if err := r.redisClient.XDel(ctx, r.lbsName(), entry.id).Err(); err != nil {
		r.logger.Warn("error deleting original LBS entry after DLQ routing",
			"error", err, "id_in_lbs", entry.id)
	}

	return reQueueDLQ, nil
}

// readLBSEntry fetches a single LBS stream entry by ID. The returned bool reports whether the entry
// still exists; a false value (with a nil error) means it was already removed from the stream.
func (r *RecoverableRedisStreamClient) readLBSEntry(
	ctx context.Context,
	idInLBS string,
) (lbsEntry, bool, error) {
	msgs, err := r.redisClient.XRange(ctx, r.lbsName(), idInLBS, idInLBS).Result()
	if err != nil {
		return lbsEntry{}, false, errs.NewRedisError(errs.OpReQueue, err)
	}
	if len(msgs) == 0 {
		return lbsEntry{}, false, nil
	}
	return lbsEntry{id: idInLBS, values: msgs[0].Values}, true, nil
}

// ownerAlive reports whether the consumer that owns a pending entry is still alive, determined by
// the presence of its distributed-lock key. A live owner means the entry must be left in place to
// avoid duplicate processing.
func (r *RecoverableRedisStreamClient) ownerAlive(ctx context.Context, lbsInfo notifs.LBSInfo) (bool, error) {
	exists, err := r.redisClient.Exists(ctx, lbsInfo.FormMutexKey()).Result()
	if err != nil {
		return false, errs.NewRedisError(errs.OpReQueue, err)
	}
	return exists > 0, nil
}

// nextReconciliationDelay returns the base interval plus a random jitter of up to
// DefaultJitterFraction of the interval, to avoid synchronized scans across consumers.
func nextReconciliationDelay(interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = configs.DefaultReconciliationInterval
	}
	maxJitter := float64(interval) * configs.DefaultJitterFraction
	if maxJitter <= 0 {
		return interval
	}
	return interval + time.Duration(rand.Int63n(int64(maxJitter)))
}
