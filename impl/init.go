package impl

import (
    "bburli/redis-stream-client-go/types"
    "context"
    "encoding/json"
    "errors"
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
		select {
		case <-ctx.Done():
			log.Println("Context is done, exiting reading LBS stream")
			return
		default:
		}

		// blocking read on LBS stream
		res := r.redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.lbsGroupName(),
			Consumer: r.consumerID,
			Streams:  []string{r.lbsName(), types.PendingMsgID},
		})

		if res.Err() != nil{
			if errors.Is(res.Err(), context.Canceled) { return }
				log.Fatal("error while reading from LBS: ", res.Err())
				return
		}

		for _, stream := range res.Val() {
			for _, message := range stream.Messages {
				// has to be an LBS message
				v, ok := message.Values[types.LBSInput]
				if !ok {
					log.Fatal("invalid message on LBS stream, must be an LBS message type")
					return
				}

				// unmarshal the message
				var lbsMessage types.LBSMessage
				if err := json.Unmarshal([]byte(v.(string)), &lbsMessage); err != nil {
					log.Fatal("error while unmarshalling LBS message")
					return
				}

				if lbsMessage.DataStreamName == "" {
					log.Fatal("invalid message type on LBS")
					return
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
					log.Fatal("unable to lock mutex")
				}

                _, err := mutex.Extend()
                if err != nil {
                    log.Fatal("error while extending lock")
					return
                }

				r.streamLocks[lbsMessage.DataStreamName] = &lbsInfo{
					DataStreamName: lbsMessage.DataStreamName,
					IDInLBS:        message.ID,
					Mutex:          mutex,
                }

				r.lbsChan <- &message

				// now, keep extending the lock in a separate go routine
				go func() {
					for {
						select {
						case <-ctx.Done():
							log.Println("context done, exiting ", r.consumerID)
							return
						default:
						}

						if _, err := mutex.Extend(); err != nil {
							log.Fatal("failed to extend lock for stream", lbsMessage.DataStreamName, "err: ", err)
							return
						}

						log.Println("extended lock for stream", lbsMessage.DataStreamName)

						time.Sleep(r.hbInterval / 2)
					}
				}()
			}
		}

		// sleep for a while
		time.Sleep(5 * time.Millisecond)
	}
}
