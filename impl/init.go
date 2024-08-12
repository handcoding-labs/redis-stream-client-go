package impl

import (
	"bburli/redis-stream-client-go/types"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
)

func (r *ReliableRedisStreamClient) enableKeyspaceNotifsForExpiredEvents(ctx context.Context) error {
	// subscribe to key space events for expiration only
	// https://redis.io/docs/latest/develop/use/keyspace-notifications/
	res := r.redisClient.ConfigSet(ctx, types.NotifyKeyspaceEventsCmd, types.KeyspacePatternForExpiredEvents)
	if res.Err() != nil {
		return res.Err()
	}

	return nil
}

func (r *ReliableRedisStreamClient) subscribeToExpiredEvents(ctx context.Context) error {
	r.pubSub = r.redisClient.PSubscribe(ctx, types.ExpiredEventPattern)
	r.kspChan = r.pubSub.Channel(redis.WithChannelHealthCheckInterval(1*time.Second), redis.WithChannelSendTimeout(10*time.Minute))
	return nil
}

func (r *ReliableRedisStreamClient) readLBSStream(ctx context.Context) {

	pool := goredis.NewPool(r.redisClient)
	rs := redsync.New(pool)

	// create group
	r.redisClient.XGroupCreateMkStream(ctx, r.lbsName(), r.lbsGroupName(), types.StartFromNow)

	for {
		// check if context is done
		if r.isContextDone(ctx) {
			log.Println("context is done, returning...")
			return
		}

		// blocking read on LBS stream
		res := r.redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.lbsGroupName(),
			Consumer: r.consumerID,
			Streams:  []string{r.lbsName(), types.PendingMsgID},
		})

		if res.Err() != nil {
			if errors.Is(res.Err(), context.Canceled) {
				return
			}
			log.Fatal("error while reading from LBS: ", res.Err())
			return
		}

		if err := r.processLBSMessages(ctx, res.Val(), rs); err != nil {
			log.Fatal("fatal error while reading lbs: ", err, "exiting...")
			return
		}

		// sleep for a while
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *ReliableRedisStreamClient) processLBSMessages(ctx context.Context, streams []redis.XStream, rs *redsync.Redsync) error {
	for _, stream := range streams {
		for _, message := range stream.Messages {
			// has to be an LBS message
			v, ok := message.Values[types.LBSInput]
			if !ok {
				return fmt.Errorf("invalid message on LBS stream, must be an LBS message type")
			}

			// unmarshal the message
			var lbsMessage types.LBSMessage
			if err := json.Unmarshal([]byte(v.(string)), &lbsMessage); err != nil {
				return fmt.Errorf("error while unmarshalling LBS message")
			}

			if lbsMessage.DataStreamName == "" {
				return fmt.Errorf("invalid message type on LBS")
			}

			// acquire lock
			mutex := rs.NewMutex(lbsMessage.DataStreamName+":"+message.ID,
				redsync.WithExpiry(r.hbInterval),
				redsync.WithFailFast(true),
				redsync.WithRetryDelay(10*time.Millisecond),
				redsync.WithSetNXOnExtend(),
				redsync.WithGenValueFunc(func() (string, error) {
					return r.consumerID, nil
				}))

			if err := mutex.Lock(); err != nil {
				return fmt.Errorf("unable to lock mutex")
			}

			_, err := mutex.Extend()
			if err != nil {
				return fmt.Errorf("error while extending lock")
			}

			r.streamLocks[lbsMessage.DataStreamName] = &lbsInfo{
				DataStreamName: lbsMessage.DataStreamName,
				IDInLBS:        message.ID,
				Mutex:          mutex,
			}

			r.lbsChan <- &message

			log.Println("wrote message ", message, " to LBS")

			// now, keep extending the lock in a separate go routine
			go r.startExtendingKey(ctx, mutex)
		}
	}

	return nil
}

func (r *ReliableRedisStreamClient) startExtendingKey(ctx context.Context, mutex *redsync.Mutex) {
	for {
		if r.isContextDone(ctx) {
			return
		}

		if _, err := mutex.Extend(); err != nil {
			log.Fatal("failed to extend lock for stream err: ", err)
			return
		}

		time.Sleep(r.hbInterval / 2)
	}
}
