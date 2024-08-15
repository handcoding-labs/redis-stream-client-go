package impl

import (
	"bburli/redis-stream-client/source/types"
	"context"
	"encoding/json"
	"fmt"
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
	sub := r.redisClient.PSubscribe(ctx, types.ExpiredEventPattern)
	r.kspChan = sub.Channel(redis.WithChannelHealthCheckInterval(1*time.Second), redis.WithChannelSendTimeout(10*time.Minute))
	return nil
}

func (r *ReliableRedisStreamClient) readLBSStream(ctx context.Context) error {
	pool := goredis.NewPool(r.redisClient)
	rs := redsync.New(pool)

	for {
		// check if context is done
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// blocking read on LBS stream
		fmt.Println("Reading from ", r.lbsName(), types.PendingMsgID)
		res := r.redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.lbsGroupName(),
			Consumer: r.consumerID,
			Streams:  []string{r.lbsName(), types.PendingMsgID},
		})

		fmt.Println("Reading LBS stream: ", res.Val(), res.Err())

		if res.Err() != nil {
			return res.Err()
		}

		for _, stream := range res.Val() {
			for _, message := range stream.Messages {
				fmt.Println("Got a new stream: ", message.Values)
				// has to be an LBS message
				v, ok := message.Values[types.LBSInput]
				if !ok {
					return fmt.Errorf("invalid message on LBS stream, must be an LBS message type")
				}

				// unmarshal the message
				var lbsMessage types.LBSMessage
				if err := json.Unmarshal([]byte(v.(string)), &lbsMessage); err != nil {
					return err
				}

				if lbsMessage.DataStreamName == "" {
					return fmt.Errorf("invalid message on LBS stream, must be an LBS message type")
				}

				// acquire lock
				mutex := rs.NewMutex(lbsMessage.DataStreamName,
					redsync.WithExpiry(r.hbInterval),
					redsync.WithFailFast(true),
					redsync.WithRetryDelay(10*time.Millisecond),
					redsync.WithSetNXOnExtend(),
					redsync.WithGenValueFunc(func() (string, error) {
						return r.consumerID, nil
					}))

				r.streamLocks[lbsMessage.DataStreamName] = &types.LBSInfo{
					DataStreamName: lbsMessage.DataStreamName,
					IDInLBS:        message.ID,
					Mutex:          mutex,
				}

				r.lbsChan <- &message
			}
		}

		// sleep for a while
		time.Sleep(10 * time.Millisecond)
	}
}
