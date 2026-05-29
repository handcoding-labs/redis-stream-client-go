package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// reQueueOutcome describes what happened when a pending LBS message was processed for recovery.
type reQueueOutcome int

const (
	// reQueueLostRace means the message was already handled by another recoverer (or no longer
	// exists / was unparseable). Nothing for this caller to do.
	reQueueLostRace reQueueOutcome = iota
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
	values, retryCount, err := r.readLBSEntry(ctx, idInLBS)
	if err != nil {
		return reQueueLostRace, "", err
	}
	if values == nil {
		// the entry no longer exists in the stream; clear any lingering PEL ownership and bail
		_ = r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), idInLBS).Err()
		return reQueueLostRace, "", nil
	}

	dataStreamName, err := dataStreamNameFromValues(values)
	if err != nil {
		// unparseable entry: ack to avoid an endless poison loop, then drop
		_ = r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), idInLBS).Err()
		r.logger.Warn("unparseable LBS entry during re-queue; acked and dropped",
			"error", err, "id_in_lbs", idInLBS)
		return reQueueLostRace, "", nil
	}

	lbsInfo, err := notifs.CreateByParts(dataStreamName, idInLBS)
	if err != nil {
		return reQueueLostRace, dataStreamName, err
	}

	// Mutex-liveness check: if the lock still exists, the owning consumer is alive (just slow).
	// Skipping here is what prevents duplicate processing of slow-but-alive consumers.
	exists, err := r.redisClient.Exists(ctx, lbsInfo.FormMutexKey()).Result()
	if err != nil {
		return reQueueLostRace, dataStreamName, errs.NewRedisError(errs.OpReQueue, err)
	}
	if exists > 0 {
		r.metricsRecorder.RecordMutexAliveSkip(dataStreamName)
		r.logger.Debug("skipping re-queue; lock still held by live consumer",
			"data_stream", dataStreamName, "mutex_key", lbsInfo.FormMutexKey())
		return reQueueSkippedAlive, dataStreamName, nil
	}

	next := retryCount + 1

	// Retries exhausted -> DLQ (or drop).
	if next > r.recoveryConfig.MaxRetries {
		out, err := r.routeToDLQ(ctx, dataStreamName, idInLBS, values, next)
		return out, dataStreamName, err
	}

	// XACK-first dedup across concurrent recoverers.
	acked, err := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), idInLBS).Result()
	if err != nil {
		r.metricsRecorder.RecordReQueue(dataStreamName, false)
		return reQueueLostRace, dataStreamName, errs.NewRedisError(errs.OpReQueue, err)
	}
	if acked == 0 {
		// someone else already acked (and presumably re-queued) this message
		return reQueueLostRace, dataStreamName, nil
	}

	// Re-add the task as a brand-new message so XREADGROUP redistributes it normally.
	newValues := cloneValues(values)
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
		return reQueueRequeued, dataStreamName, errs.NewRedisError(errs.OpReQueue, err)
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

// routeToDLQ acknowledges a poison message and either re-adds it to the configured DLQ stream or
// drops it (when no DLQ is configured). Like reQueue it is XACK-first for cross-recoverer dedup.
func (r *RecoverableRedisStreamClient) routeToDLQ(
	ctx context.Context,
	dataStreamName, idInLBS string,
	values map[string]interface{},
	retryCount int,
) (reQueueOutcome, error) {
	acked, err := r.redisClient.XAck(ctx, r.lbsName(), r.lbsGroupName(), idInLBS).Result()
	if err != nil {
		return reQueueLostRace, errs.NewRedisError(errs.OpRouteDLQ, err)
	}
	if acked == 0 {
		return reQueueLostRace, nil
	}

	if r.recoveryConfig.DLQStream != "" {
		dlqValues := cloneValues(values)
		dlqValues[configs.RetryCountField] = strconv.Itoa(retryCount)
		dlqValues[configs.DLQReasonField] = configs.DLQReasonMaxRetries
		if err := r.redisClient.XAdd(ctx, &redis.XAddArgs{
			Stream: r.recoveryConfig.DLQStream,
			Values: dlqValues,
		}).Err(); err != nil {
			r.metricsRecorder.RecordAckAddGap(dataStreamName)
			r.logger.Error("XADD to DLQ failed after XACK; message dropped (atomicity gap)",
				"error", err, "data_stream", dataStreamName, "dlq", r.recoveryConfig.DLQStream)
			return reQueueDLQ, errs.NewRedisError(errs.OpRouteDLQ, err)
		}
		r.metricsRecorder.RecordDLQRouting(dataStreamName)
		r.logger.Warn("routed message to DLQ after exceeding max retries",
			"data_stream", dataStreamName, "retry_count", retryCount, "dlq", r.recoveryConfig.DLQStream)
	} else {
		r.logger.Warn("dropping message after exceeding max retries (no DLQ configured)",
			"data_stream", dataStreamName, "retry_count", retryCount)
	}

	if err := r.redisClient.XDel(ctx, r.lbsName(), idInLBS).Err(); err != nil {
		r.logger.Warn("error deleting original LBS entry after DLQ routing",
			"error", err, "id_in_lbs", idInLBS)
	}

	return reQueueDLQ, nil
}

// readLBSEntry fetches a single LBS stream entry by ID, returning its field/value map and the
// parsed retry count. A nil map (with nil error) means the entry no longer exists.
func (r *RecoverableRedisStreamClient) readLBSEntry(ctx context.Context, idInLBS string) (map[string]interface{}, int, error) {
	msgs, err := r.redisClient.XRange(ctx, r.lbsName(), idInLBS, idInLBS).Result()
	if err != nil {
		return nil, 0, errs.NewRedisError(errs.OpReQueue, err)
	}
	if len(msgs) == 0 {
		return nil, 0, nil
	}
	values := msgs[0].Values
	return values, parseRetryCount(values), nil
}

// dataStreamNameFromValues extracts the data stream name from an LBS entry's `lbs-input` payload.
func dataStreamNameFromValues(values map[string]interface{}) (string, error) {
	v, ok := values[configs.LBSInput]
	if !ok {
		return "", errs.ErrInvalidKeyForLBSMessage
	}
	s, ok := v.(string)
	if !ok {
		return "", errs.ErrInvalidLBSMessage
	}

	var msg notifs.LBSInputMessage
	if err := json.Unmarshal([]byte(s), &msg); err != nil {
		return "", errs.NewRedisError(errs.OpUnmarshalLBSMessage, err)
	}
	if msg.DataStreamName == "" {
		return "", errs.ErrNoDatastreamInLBSMessage
	}
	return msg.DataStreamName, nil
}

// parseRetryCount reads the `_retry_count` field from an LBS entry, defaulting to 0 when absent or
// malformed (e.g. fresh messages produced by clients that are unaware of the field).
func parseRetryCount(values map[string]interface{}) int {
	v, ok := values[configs.RetryCountField]
	if !ok {
		return 0
	}
	s, ok := v.(string)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// cloneValues returns a shallow copy of an LBS entry's field/value map (with room for one extra
// field) so the original is not mutated when re-queuing or routing to the DLQ.
func cloneValues(values map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(values)+1)
	for k, v := range values {
		out[k] = v
	}
	return out
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

func (r *RecoverableRedisStreamClient) lbsGroupName() string {
	return r.serviceName + configs.GroupSuffix
}

func (r *RecoverableRedisStreamClient) lbsName() string {
	return r.serviceName + configs.InputSuffix
}

func (r *RecoverableRedisStreamClient) isContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (r *RecoverableRedisStreamClient) cleanup() error {
	// drain kspchan and ignore expired notifications
	// since client has called Done and thus are no longer interested in expired notifications
	for len(r.kspChan) > 0 {
		<-r.kspChan
	}

	// close the output channel
	r.notificationBroker.Close()

	// cancel LBS context
	r.lbsCtxCancelFunc()

	// close single-shard pub sub (nil in ClusterModeOSS)
	if r.pubSub != nil {
		if err := r.pubSub.Close(); err != nil {
			r.logger.Error("error closing redis pub sub")
			return errs.NewRedisError(errs.OpClosePubSub, err)
		}
	}

	// close any per-master subscriptions opened in ClusterModeOSS
	r.closeOSSPubSubs()

	return nil
}

// popStreamLocksInfo removes the datastream from streamLocks map (internal state) and returns the value
func (r *RecoverableRedisStreamClient) popStreamLocksInfo(dataStreamName string) (*StreamLocksInfo, error) {
	r.streamLocksMutex.Lock()
	streamLocksInfo, ok := r.streamLocks[dataStreamName]
	if !ok {
		r.streamLocksMutex.Unlock()
		return nil, errs.ErrDataStreamNotFound
	}

	// delete volatile key from streamLocks
	delete(r.streamLocks, dataStreamName)
	r.streamLocksMutex.Unlock()

	return streamLocksInfo, nil
}

func (r *RecoverableRedisStreamClient) isStreamProcessingDone(dataStreamName string) bool {
	r.streamLocksMutex.Lock()
	defer r.streamLocksMutex.Unlock()
	return r.streamLocks[dataStreamName] == nil
}

func (r *RecoverableRedisStreamClient) extractStreamnameFromKspChannel(kspChannelString string) (string, error) {
	streamName, ok := strings.CutPrefix(kspChannelString, configs.KeySpacePrefix)
	if !ok {
		return "", fmt.Errorf("invalid ksp channel payload")
	}

	return streamName, nil
}
