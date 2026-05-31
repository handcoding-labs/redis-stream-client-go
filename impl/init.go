package impl

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"

	"github.com/go-redsync/redsync/v4"
	"github.com/redis/go-redis/v9"
)

func (r *RecoverableRedisStreamClient) enableKeyspaceNotifsForExpiredEvents(ctx context.Context) error {
	// subscribe to key space events for expiration only
	// https://redis.io/docs/latest/develop/use/keyspace-notifications/
	//
	// In ClusterModeOSS the config must be applied to every master, since keyspace notifications are
	// produced by the node that owns the expiring key.
	if r.clusterMode == ClusterModeOSS {
		cluster, ok := r.redisClient.(*redis.ClusterClient)
		if !ok {
			return errs.ErrClusterClientRequired
		}
		return r.enableKeyspaceNotifsOnMasters(ctx, cluster)
	}

	return r.enableKeyspaceNotifsOn(ctx, r.redisClient)
}

// enableKeyspaceNotifsOn applies the expired-events keyspace config to a single Redis endpoint.
func (r *RecoverableRedisStreamClient) enableKeyspaceNotifsOn(ctx context.Context, client redis.Cmdable) error {
	existingConfig := client.ConfigGet(ctx, configs.NotifyKeyspaceEventsCmd)
	configVals, err := existingConfig.Result()
	if err != nil {
		return errs.NewRedisError(errs.OpEnableKeyspaceNotification, err)
	}

	for _, v := range configVals {
		if len(v) > 0 {
			// some config for key space notifications already exists, so exit
			if !r.forceOverrideConfig {
				return errs.ErrExistingConfigWithoutOverride
			} else {
				r.logger.Warn("overriding existing keyspace notifications config since force override is set")
			}
		}
	}

	res := client.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents)
	if res.Err() != nil {
		return res.Err()
	}

	return nil
}

func (r *RecoverableRedisStreamClient) subscribeToExpiredEvents(ctx context.Context) {
	// In ClusterModeOSS, keyspace notifications fire only on the master that owns the expiring key,
	// so subscribe on every master and fan all subscriptions into the single kspChan.
	if r.clusterMode == ClusterModeOSS {
		r.subscribeToExpiredEventsOSS(ctx)
		return
	}

	r.pubSub = r.redisClient.PSubscribe(ctx, configs.MutexKeySpacePattern)
	r.fanInPubSub(r.pubSub)
}

// fanInPubSub relays messages from a single pub/sub subscription into the shared kspChan, dropping
// (with a metric) when kspChan is full so a slow consumer never blocks the pub/sub reader.
func (r *RecoverableRedisStreamClient) fanInPubSub(pubSub *redis.PubSub) {
	redisPubSubChan := pubSub.Channel(
		redis.WithChannelHealthCheckInterval(5*time.Second),
		redis.WithChannelSendTimeout(r.kspChanTimeout),
		redis.WithChannelSize(r.kspChanSize),
	)

	go func() {
		for msg := range redisPubSubChan {
			select {
			case r.kspChan <- msg:
				// message sent successfully to kspChan
			default:
				// kspChan is full or blocked, log the timeout and drop the message
				r.logger.Warn("kspChan is full or blocked, dropping ksp notification",
					"channel", msg.Channel, "payload", msg.Payload)
				r.metricsRecorder.RecordKspNotificationDropped()
			}
		}
	}()
}

// runReconciliationLoop periodically recovers pending LBS messages whose owning consumer has died.
//
// It replaces the old XAUTOCLAIM-based startup recovery. The first pass runs immediately (covering
// startup recovery), then subsequent passes run every ReconciliationInterval plus jitter. This is
// the authoritative, cluster-safe recovery mechanism: it inspects the group's pending entries,
// verifies the owning consumer is dead (its lock key is gone), and re-queues the work via XACK+XADD.
func (r *RecoverableRedisStreamClient) runReconciliationLoop(ctx context.Context) {
	// immediate first pass (startup recovery)
	r.reconcileLBS(ctx, true)

	for {
		select {
		case <-ctx.Done():
			r.logger.Debug("context done, stopping reconciliation loop", "consumer_id", r.consumerID)
			return
		case <-time.After(nextReconciliationDelay(r.recoveryConfig.ReconciliationInterval)):
			r.reconcileLBS(ctx, false)
		}
	}
}

// reconcileLBS performs a single reconciliation pass: it reads up to BatchSize pending LBS messages
// that have been idle for at least MinIdleTime and re-queues those whose owning consumer is dead.
// Errors are logged (not fatal) so a transient failure does not stop the consumer. The first pass
// (startup==true) also records the startup-recovery metric, since it replaces the old XAUTOCLAIM
// startup recovery.
func (r *RecoverableRedisStreamClient) reconcileLBS(ctx context.Context, startup bool) {
	start := time.Now()
	var requeued, skippedAlive, dlqRouted int

	pending, err := r.redisClient.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: r.lbsName(),
		Group:  r.lbsGroupName(),
		Idle:   r.recoveryConfig.MinIdleTime,
		Start:  configs.MinimalRangeID,
		End:    configs.MaximalRangeID,
		Count:  int64(r.recoveryConfig.BatchSize),
	}).Result()
	if err != nil {
		r.logger.Warn("error reading pending LBS messages during reconciliation", "error", err)
		r.metricsRecorder.RecordReconciliationScan(0, 0, 0, time.Since(start))
		if startup {
			r.metricsRecorder.RecordStartupRecovery(false, 0, time.Since(start))
		}
		return
	}

	for _, p := range pending {
		if r.isContextDone(ctx) {
			return
		}

		outcome, dataStreamName, err := r.reQueue(ctx, p.ID)
		if err != nil {
			r.logger.Warn("error re-queuing pending message during reconciliation",
				"error", err, "id_in_lbs", p.ID, "data_stream", dataStreamName)
			continue
		}

		switch outcome {
		case reQueueRequeued:
			requeued++
		case reQueueSkippedAlive:
			skippedAlive++
		case reQueueDLQ:
			dlqRouted++
		}
	}

	r.metricsRecorder.RecordReconciliationScan(requeued, skippedAlive, dlqRouted, time.Since(start))
	if startup {
		r.metricsRecorder.RecordStartupRecovery(true, requeued+dlqRouted, time.Since(start))
		r.logger.Info("startup reconciliation scan complete",
			"consumer_id", r.consumerID, "requeued", requeued, "skipped_alive", skippedAlive,
			"dlq_routed", dlqRouted, "pending_inspected", len(pending))
	}
	if requeued > 0 || dlqRouted > 0 || skippedAlive > 0 {
		r.logger.Info("reconciliation scan complete",
			"requeued", requeued, "skipped_alive", skippedAlive, "dlq_routed", dlqRouted,
			"duration_seconds", time.Since(start).Seconds())
	}
}

func (r *RecoverableRedisStreamClient) readLBSStream(ctx context.Context) {
	consecutiveErrors := 0
	currentRetryDelay := r.initialRetryDelay

	for {
		// check if context is done
		if r.isContextDone(ctx) {
			r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif("context done"))
			return
		}

		// blocking read on LBS stream
		res := r.redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.lbsGroupName(),
			Consumer: r.consumerID,
			Streams:  []string{r.lbsName(), configs.PendingMsgID},
			Block:    0,
		})

		if res.Err() != nil {
			if errors.Is(res.Err(), context.Canceled) {
				r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif(context.Canceled.Error()))
				return
			}

			consecutiveErrors++
			r.logger.Error("error while reading from LBS",
				"error", res.Err(),
				"consecutive_errors", consecutiveErrors,
				"retry_delay", currentRetryDelay)

			if r.maxRetries >= 0 && consecutiveErrors > r.maxRetries {
				r.logger.Error("max retries exceeded, terminating stream",
					"max_retries", r.maxRetries,
					"consecutive_errors", consecutiveErrors)
				r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif(res.Err().Error()))
				return
			}

			// sleep with exponential backoff before retrying
			select {
			case <-ctx.Done():
				r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif(context.Canceled.Error()))
				return
			case <-time.After(currentRetryDelay):
				// calculate next retry delay with exponential backoff
				currentRetryDelay = currentRetryDelay * 2
				if currentRetryDelay > r.maxRetryDelay {
					currentRetryDelay = r.maxRetryDelay
				}
			}

			continue
		}

		// successful read - reset error tracking
		if consecutiveErrors > 0 {
			r.logger.Info("LBS stream read recovered after errors",
				"consecutive_errors", consecutiveErrors)
			consecutiveErrors = 0
			currentRetryDelay = r.initialRetryDelay
		}

		if err := r.processLBSMessages(ctx, res.Val(), r.rs); err != nil {
			r.logger.Error("fatal error while processing lbs messages", "error", err)
			r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif(err.Error()))
			return
		}
	}
}

func (r *RecoverableRedisStreamClient) processLBSMessages(
	ctx context.Context,
	streams []redis.XStream,
	rs *redsync.Redsync,
) error {
	for _, stream := range streams {
		for _, message := range stream.Messages {
			// has to be an LBS message
			v, ok := message.Values[configs.LBSInput]
			if !ok {
				return errs.ErrInvalidKeyForLBSMessage
			}

			// unmarshal the message
			var lbsMessage notifs.LBSInputMessage
			val, ok := v.(string)
			if !ok {
				return errs.ErrInvalidLBSMessage
			}

			if err := json.Unmarshal([]byte(val), &lbsMessage); err != nil {
				return errs.NewRedisError(errs.OpUnmarshalLBSMessage, err)
			}

			if lbsMessage.DataStreamName == "" {
				return errs.ErrNoDatastreamInLBSMessage
			}

			lbsInfo, err := notifs.CreateByParts(lbsMessage.DataStreamName, message.ID)
			if err != nil {
				return err
			}

			// create mutex
			redsyncMutex := rs.NewMutex(lbsInfo.FormMutexKey(),
				redsync.WithExpiry(r.hbInterval),
				redsync.WithFailFast(true),
				redsync.WithRetryDelay(10*time.Millisecond),
				redsync.WithSetNXOnExtend(),
				redsync.WithGenValueFunc(func() (string, error) {
					return r.consumerID, nil
				}))

			// lock only once
			start := time.Now()
			if err := redsyncMutex.Lock(); err != nil {
				r.metricsRecorder.RecordLockAcquisitionAttempt(lbsMessage.DataStreamName, false, time.Since(start))
				return errs.NewMutexError(errs.OpLockMutex, err)
			}
			r.metricsRecorder.RecordLockAcquisitionAttempt(lbsMessage.DataStreamName, true, time.Since(start))

			r.streamLocksMutex.Lock()
			r.streamLocks[lbsInfo.DataStreamName] = &StreamLocksInfo{
				LBSInfo:        lbsInfo,
				RedsyncMutex:   redsyncMutex,
				AdditionalInfo: lbsMessage.Info,
			}
			r.streamLocksMutex.Unlock()

			r.notificationBroker.Send(ctx, notifs.Make(notifs.StreamAdded, lbsInfo, lbsMessage.Info))
			r.metricsRecorder.RecordStreamProcessingStart(lbsInfo.DataStreamName, time.Now())

			// now, keep extending the lock in a separate go routine
			go func() {
				if err := r.startExtendingKey(ctx, redsyncMutex, lbsInfo, lbsMessage.Info); err != nil {
					r.logger.Error("Error extending key", "error", err, "stream", lbsInfo.DataStreamName)
				}
			}()
		}
	}

	return nil
}

func (r *RecoverableRedisStreamClient) startExtendingKey(
	ctx context.Context,
	redsyncMutex *redsync.Mutex,
	lbsInfo notifs.LBSInfo,
	additionalInfo map[string]any,
) error {
	extensionFailed := false
	defer func() {
		if extensionFailed {
			// if client is still interested or is coming back from a delay (GC pause etc) then inform about disowning of stream
			r.notificationBroker.Send(ctx, notifs.Make(notifs.StreamDisowned, lbsInfo, additionalInfo))
		}

		// if stream processing is not finished at this point, pop stream to prevent
		// the internal map from getting polluted.
		if !r.isStreamProcessingDone(lbsInfo.DataStreamName) {
			_, err := r.popStreamLocksInfo(lbsInfo.DataStreamName)
			if err != nil {
				r.logger.Warn("error cleaning up internal state", "error", err)
			}
		}
	}()

	for {
		// exit extending the key if:
		// main context is canceled
		if r.isContextDone(ctx) {
			r.logger.Info("context done, exiting", "consumer_id", r.consumerID)
			return nil
		}

		// or if DoneStream was called
		if r.isStreamProcessingDone(lbsInfo.DataStreamName) {
			r.logger.Debug("DoneStream called. Stopping key extension.")
			return nil
		}

		if ok, err := redsyncMutex.Extend(); !ok || err != nil {
			extensionFailed = true
			r.metricsRecorder.RecordLockExtensionAttempt(lbsInfo.DataStreamName, false)
			return errs.NewMutexError(errs.OpExtendMutex, err)
		}
		r.metricsRecorder.RecordLockExtensionAttempt(lbsInfo.DataStreamName, true)

		time.Sleep(r.hbInterval / 2)
	}
}

func (r *RecoverableRedisStreamClient) listenKsp(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.logger.Debug("context done, exiting", "consumer_id", r.consumerID)
			return
		case kspNotif := <-r.kspChan:
			if kspNotif != nil {
				r.logger.Debug("ksp notif received", "consumer_id", r.consumerID, "payload", kspNotif.Payload)
				streamName, err := r.extractStreamnameFromKspChannel(kspNotif.Channel)
				if err != nil {
					r.logger.Warn("error extracting stream name from ksp channel", "ksp_channel", kspNotif.Channel, "error", err)
					continue
				}

				// record the ksp notification metric
				r.metricsRecorder.RecordKspNotification(streamName)

				lbsInfo, err := notifs.CreateByKspNotification(streamName, kspNotif.Payload)
				if err != nil {
					r.logger.Warn("error parsing ksp notification", "ksp_notification", kspNotif)
					continue
				}

				// Try to get additional info from stored stream locks
				var additionalInfo map[string]any
				r.streamLocksMutex.RLock()
				if streamLockInfo, exists := r.streamLocks[lbsInfo.DataStreamName]; exists {
					additionalInfo = streamLockInfo.AdditionalInfo
				}
				r.streamLocksMutex.RUnlock()

				r.notificationBroker.Send(ctx, notifs.Make(notifs.StreamExpired, lbsInfo, additionalInfo))
			}
		}
	}
}
