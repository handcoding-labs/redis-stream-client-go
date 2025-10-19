package impl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"

	"github.com/go-redsync/redsync/v4"
	"github.com/redis/go-redis/v9"
)

func (r *RecoverableRedisStreamClient) enableKeyspaceNotifsForExpiredEvents(ctx context.Context) error {
	// subscribe to key space events for expiration only
	// https://redis.io/docs/latest/develop/use/keyspace-notifications/
	res := r.redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents)
	if res.Err() != nil {
		return res.Err()
	}

	return nil
}

func (r *RecoverableRedisStreamClient) subscribeToExpiredEvents(ctx context.Context) error {
	r.pubSub = r.redisClient.PSubscribe(ctx, configs.ExpiredEventPattern)
	r.kspChan = r.pubSub.Channel(
		redis.WithChannelHealthCheckInterval(1*time.Second),
		redis.WithChannelSendTimeout(10*time.Minute),
	)
	return nil
}

// This method doesn't return error and just logs because we execute this
// when no consumer was around to recevie notifications and messages were pending.
// So, this method is just recovering those messages and if there is an issue in
// processing them, then erroring out will stop consumer from processing latest streams also.
func (r *RecoverableRedisStreamClient) recoverUnackedLBS(ctx context.Context) {
	xpendingCmdRes := r.redisClient.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: r.lbsName(),
		Group:  r.lbsGroupName(),
		Idle:   r.lbsIdleTime,
		Start:  configs.MinimalRangeID,
		End:    configs.MaximalRangeID,
		Count:  int64(r.lbsRecoveryCount),
	})

	if xpendingCmdRes.Err() != nil {
		r.logger.Error("error while getting unacked messages", "error", xpendingCmdRes.Err())
		return
	}

	xpendingInfo := xpendingCmdRes.Val()
	if len(xpendingInfo) == 0 {
		r.logger.Info("no unacked messages found in LBS for consumer, skipping recovery")
		return
	}

	r.logger.Info("unacked messages found in LBS for consumer", "pending_count", len(xpendingInfo))

	// XREADGROUP ensures that no two consumers get the same message
	xreadCmdRes := r.redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    r.lbsGroupName(),
		Consumer: r.consumerID,
		Streams:  []string{r.lbsName(), configs.PendingMsgID},
		Block:    0,
	})

	if xreadCmdRes.Err() != nil {
		r.logger.Error("error reading recoverable messages in lbs, skipping recovery")
		return
	}

	result := xreadCmdRes.Val()
	// we expect only one stream which is lbs here
	if len(result) != 1 {
		r.logger.Warn("found more than one entry while reading lbs!, picking the first")
	}

	streams := []redis.XStream{
		{
			Stream:   r.lbsName(),
			Messages: result[0].Messages,
		},
	}

	// process the message
	if err := r.processLBSMessages(ctx, streams, r.rs); err != nil {
		r.logger.Error("fatal error while processing unacked messages", "error", err)
		return
	}
}

func (r *RecoverableRedisStreamClient) readLBSStream(ctx context.Context) {
	for {
		// check if context is done
		if r.isContextDone(ctx) {
			if !r.outputChanClosed.Load() {
				r.outputChan <- notifs.MakeStreamTerminatedNotif("context done")
			}
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
				if !r.outputChanClosed.Load() {
					r.outputChan <- notifs.MakeStreamTerminatedNotif(context.Canceled.Error())
				}
				return
			}
			r.logger.Error("error while reading from LBS", "error", res.Err())
			if !r.outputChanClosed.Load() {
				r.outputChan <- notifs.MakeStreamTerminatedNotif(res.Err().Error())
			}
			return
		}

		if err := r.processLBSMessages(ctx, res.Val(), r.rs); err != nil {
			r.logger.Error("fatal error while reading lbs", "error", err)
			if !r.outputChanClosed.Load() {
				r.outputChan <- notifs.MakeStreamTerminatedNotif(err.Error())
			}
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
				return fmt.Errorf("message on LBS stream must be keyed with %s", configs.LBSInput)
			}

			// unmarshal the message
			var lbsMessage notifs.LBSInputMessage
			if err := json.Unmarshal([]byte(v.(string)), &lbsMessage); err != nil {
				return fmt.Errorf("error while unmarshalling LBS message: %w", err)
			}

			if lbsMessage.DataStreamName == "" {
				return fmt.Errorf("no data stream specified in LBS message")
			}

			lbsInfo, err := notifs.CreateByParts(lbsMessage.DataStreamName, message.ID)
			if err != nil {
				return err
			}

			// create mutex
			mutex := rs.NewMutex(lbsInfo.FormMutexKey(),
				redsync.WithExpiry(r.hbInterval),
				redsync.WithFailFast(true),
				redsync.WithRetryDelay(10*time.Millisecond),
				redsync.WithSetNXOnExtend(),
				redsync.WithGenValueFunc(func() (string, error) {
					return r.consumerID, nil
				}))

			// lock only once
			if err := mutex.Lock(); err != nil {
				return err
			}

			r.streamLocksMutex.Lock()
			r.streamLocks[lbsInfo.DataStreamName] = &StreamLocksInfo{
				LBSInfo:        lbsInfo,
				Mutex:          mutex,
				AdditionalInfo: lbsMessage.Info,
			}
			r.streamLocksMutex.Unlock()
			if !r.outputChanClosed.Load() {
				r.outputChan <- notifs.Make(notifs.StreamAdded, lbsInfo, lbsMessage.Info)
			}

			// now, keep extending the lock in a separate go routine
			go func() {
				if err := r.startExtendingKey(ctx, mutex, lbsInfo, lbsMessage.Info); err != nil {
					r.logger.Error("Error extending key", "error", err, "stream", lbsInfo.DataStreamName)
				}
			}()
		}
	}

	return nil
}

func (r *RecoverableRedisStreamClient) startExtendingKey(
	ctx context.Context,
	mutex *redsync.Mutex,
	lbsInfo notifs.LBSInfo,
	additionalInfo map[string]any,
) error {
	extensionFailed := false
	defer func() {
		if extensionFailed && !r.outputChanClosed.Load() {
			// if client is still interested or is coming back from a delay (GC pause etc) then inform about disowning of stream
			r.outputChan <- notifs.Make(notifs.StreamDisowned, lbsInfo, additionalInfo)
		}
	}()

	for {
		// exit extending the key if:
		// main context is canceled
		if r.isContextDone(ctx) {
			r.logger.Debug("context done, exiting", "consumer_id", r.consumerID)
			return nil
		}

		// or if DoneStream was called
		if r.isStreamProcessingDone(lbsInfo.DataStreamName) {
			r.logger.Debug("DoneStream called. Stopping key extension.")
			return nil
		}

		if ok, err := mutex.Extend(); !ok || err != nil {
			extensionFailed = true
			return fmt.Errorf("could not extend mutex, err: %s", err)
		}

		time.Sleep(r.hbInterval / 2)
	}
}

func (r *RecoverableRedisStreamClient) listenKsp(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Debug("context done, exiting", "consumer_id", r.consumerID)
			if !r.outputChanClosed.Load() {
				r.outputChan <- notifs.MakeStreamTerminatedNotif("context done")
			}
			return
		case kspNotif := <-r.kspChan:
			if kspNotif != nil {
				r.logger.Debug("ksp notif received", "consumer_id", r.consumerID, "payload", kspNotif.Payload)
				lbsInfo, err := notifs.CreateByKspNotification(kspNotif.Payload)
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
				if !r.outputChanClosed.Load() {
					r.outputChan <- notifs.Make(notifs.StreamExpired, lbsInfo, additionalInfo)
				}
			}
		case <-ticker.C:
			// check if the channel is closed,
			// this means that the client has called Done and is no longer interested in expired notifications
			if r.outputChanClosed.Load() {
				r.logger.Debug("output channel closed, exiting", "consumer_id", r.consumerID)
				return
			}
		}
	}
}
