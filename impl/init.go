package impl

import (
	"bburli/redis-stream-client-go/types"
	"context"
	"encoding/json"
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

func (r *ReliableRedisStreamClient) readLBSStream(ctx context.Context) error {
	pool := goredis.NewPool(r.redisClient)
	rs := redsync.New(pool)

	// create group
	r.redisClient.XGroupCreateMkStream(ctx, r.lbsName(), r.lbsGroupName(), types.StartFromNow)

	for {
		// check if context is done
		select {
		case <-ctx.Done():
			log.Println("Context is done, existing reading LBS stream")
		default:
		}

		// blocking read on LBS stream
		log.Println("Reading LBS stream")
		res := r.redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.lbsGroupName(),
			Consumer: r.consumerID,
			Streams:  []string{r.lbsName(), types.PendingMsgID},
		})

		if res.Err() != nil {
			return res.Err()
		}

		for _, stream := range res.Val() {
			for _, message := range stream.Messages {
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

				// now, keep extending the lock in a separate go routine
				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						default:
						}

						if _, err := mutex.Extend(); err != nil {
							log.Println("Failed to extend lock for stream", lbsMessage.DataStreamName, "err: ", err)
							return
						}

						log.Println("Extended lock for stream", lbsMessage.DataStreamName)

						time.Sleep(r.hbInterval / 2)
					}
				}()

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
