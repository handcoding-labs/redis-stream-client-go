package impl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	r.kspChan = r.pubSub.Channel(redis.WithChannelHealthCheckInterval(1*time.Second), redis.WithChannelSendTimeout(10*time.Minute))
	return nil
}

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
		log.Fatal("error while getting unacked messages: ", xpendingCmdRes.Err())
		return
	}

	xpendingInfo := xpendingCmdRes.Val()
	if len(xpendingInfo) == 0 {
		log.Println("no unacked messages found in LBS for consumer, skipping recovery")
		return
	}

	log.Println("unacked messages found in LBS for consumer: ", xpendingInfo)

	xrangeCmdRes := r.redisClient.XRange(ctx, r.lbsName(), xpendingInfo[0].ID, xpendingInfo[len(xpendingInfo)-1].ID)
	if xrangeCmdRes.Err() != nil {
		log.Fatal("error while getting unacked messages: ", xrangeCmdRes.Err())
		return
	}

	streams := []redis.XStream{
		{
			Stream:   r.lbsName(),
			Messages: xrangeCmdRes.Val(),
		},
	}

	// process the message
	if err := r.processLBSMessages(ctx, streams, r.rs); err != nil {
		log.Fatal("fatal error while processing unacked messages: ", err, "exiting...")
		return
	}
}

func (r *RecoverableRedisStreamClient) readLBSStream(ctx context.Context) {
	for {
		// check if context is done
		if r.isContextDone(ctx) {
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
				return
			}
			log.Fatal("error while reading from LBS: ", res.Err())
			return
		}

		if err := r.processLBSMessages(ctx, res.Val(), r.rs); err != nil {
			log.Fatal("fatal error while reading lbs: ", err, "exiting...")
			return
		}

	}
}

func (r *RecoverableRedisStreamClient) processLBSMessages(ctx context.Context, streams []redis.XStream, rs *redsync.Redsync) error {
	for _, stream := range streams {
		for _, message := range stream.Messages {
			// has to be an LBS message
			v, ok := message.Values[configs.LBSInput]
			if !ok {
				return fmt.Errorf("invalid message on LBS stream, must be an LBS message type")
			}

			// unmarshal the message
			var lbsMessage notifs.LBSMessage
			if err := json.Unmarshal([]byte(v.(string)), &lbsMessage); err != nil {
				return fmt.Errorf("error while unmarshalling LBS message")
			}

			if lbsMessage.DataStreamName == "" {
				return fmt.Errorf("invalid message type on LBS")
			}

			lbsInfo, err := createByParts(lbsMessage.DataStreamName, message.ID)
			if err != nil {
				return err
			}

			// create mutex
			mutex := rs.NewMutex(lbsInfo.getMutexKey(),
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

			// now seed the mutex
			lbsInfo.Mutex = mutex

			r.streamLocks[lbsInfo.DataStreamName] = lbsInfo
			r.outputChan <- notifs.Make(v, notifs.StreamAdded)

			// now, keep extending the lock in a separate go routine
			go r.startExtendingKey(ctx, mutex, lbsInfo.DataStreamName)
		}
	}

	return nil
}

func (r *RecoverableRedisStreamClient) startExtendingKey(ctx context.Context, mutex *redsync.Mutex, streamName string) error {
	extensionFailed := false
	defer func() {
		if extensionFailed && !r.outputChanClosed.Load() {
			// if client is still interested or is coming back from a delay (GC pause etc) then inform about disowning of stream
			r.outputChan <- notifs.Make(streamName, notifs.StreamDisowned)
		}
	}()

	for {
		if r.isContextDone(ctx) {
			return nil
		}

		if ok, err := mutex.Extend(); !ok || err != nil {
			extensionFailed = true
			return fmt.Errorf("could not extend mutex, err: %s", err)
		}

		time.Sleep(r.hbInterval / 2)
	}
}

func (r *RecoverableRedisStreamClient) listenKsp() {
	for kspNotif := range r.kspChan {
		r.outputChan <- notifs.Make(kspNotif.Payload, notifs.StreamExpired)
	}
}
