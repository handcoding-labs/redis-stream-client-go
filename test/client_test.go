package test

import (
	"bburli/redis-stream-client-go/impl"
	"bburli/redis-stream-client-go/types"
	"context"
	"encoding/json"
	"log"
	"os"
	"testing"
	"time"

	redisgo "github.com/redis/go-redis/v9"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

func newRedisClient(redisContainer *redis.RedisContainer) redisgo.UniversalClient {
	connString, err := redisContainer.ConnectionString(context.Background())
	if err != nil {
		panic(err)
	}

	connString = connString[8:] // remove redis:// prefix

	return redisgo.NewUniversalClient(&redisgo.UniversalOptions{
		Addrs: []string{connString},
		DB:    0,
	})
}

func setupSuite(t *testing.T) *redis.RedisContainer {
	redisContainer, err := redis.Run(context.Background(), "redis:7.2.3")
	if err != nil {
		t.Fatalf("failed to start redis container: %v", err)
	}
	require.True(t, redisContainer != nil)
	require.True(t, redisContainer.IsRunning())

	connString, err := redisContainer.ConnectionString(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, connString)

	return redisContainer
}

func TestLBS(t *testing.T) {
	ctx := context.TODO()
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	res := redisClient.ConfigSet(ctx, types.NotifyKeyspaceEventsCmd, types.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	// create consumer1 client
	consumer1 := createConsumer("111", redisContainer)
	require.NotNil(t, consumer1)
	lbsChan1, kspChan1, err := consumer1.Init(ctx)
	require.NoError(t, err)
	require.NotNil(t, lbsChan1)
	require.NotNil(t, kspChan1)

	// create consumer2 client
	consumer2 := createConsumer("222", redisContainer)
	require.NotNil(t, consumer2)
	lbsChan2, kspChan2, err := consumer2.Init(ctx)
	require.NoError(t, err)
	require.NotNil(t, lbsChan2)
	require.NotNil(t, kspChan2)

	lbsChan1, _, err = consumer1.Init(ctx)
	require.NoError(t, err)

	lbsChan2, _, err = consumer2.Init(ctx)
	require.NoError(t, err)

	lbsMsg1, _ := json.Marshal(types.LBSMessage{
		DataStreamName: "session1",
		Info: map[string]interface{}{
			"key1": "value1",
		},
	})

	lbsMsg2, _ := json.Marshal(types.LBSMessage{
		DataStreamName: "session2",
		Info: map[string]interface{}{
			"key2": "value2",
		},
	})

	producer := newRedisClient(redisContainer)
	_, err = producer.XAdd(context.Background(), &redisgo.XAddArgs{
		Stream: "consumer-input",
		Values: map[string]any{
			types.LBSInput: string(lbsMsg1),
		},
	}).Result()
	require.NoError(t, err)

	_, err = producer.XAdd(context.Background(), &redisgo.XAddArgs{
		Stream: "consumer-input",
		Values: map[string]any{
			types.LBSInput: string(lbsMsg2),
		},
	}).Result()
	require.NoError(t, err)

	// load balanced stream distributes messages to different consumers in a load balanced way
	// so we keep track of which stream was given to consumer1 so that we can check if consumer2 gets another one
	var expectedMsgConsumer2 string
	var expectedMsgConsumer1 string

	for i := range 2 {
		log.Println("iteration: ", i)
		select {
		case msg, ok := <-lbsChan1:
			require.True(t, ok)
			require.NotNil(t, msg)
			var lbsMessage types.LBSMessage
			require.NoError(t, json.Unmarshal([]byte(msg.Values[types.LBSInput].(string)), &lbsMessage))
			require.NotNil(t, lbsMessage)

			if expectedMsgConsumer1 != "" {
				require.Equal(t, lbsMessage.DataStreamName, expectedMsgConsumer1)
			} else {
				if lbsMessage.DataStreamName == "session1" {
					expectedMsgConsumer2 = "session2"
					require.Equal(t, lbsMessage.Info["key1"], "value1")
				} else {
					expectedMsgConsumer2 = "session1"
					require.Equal(t, lbsMessage.Info["key2"], "value2")
				}
			}
		case msg, ok := <-lbsChan2:
			require.True(t, ok)
			require.NotNil(t, msg)
			var lbsMessage types.LBSMessage
			require.NoError(t, json.Unmarshal([]byte(msg.Values[types.LBSInput].(string)), &lbsMessage))
			require.NotNil(t, lbsMessage)
			if expectedMsgConsumer2 != "" {
				require.Equal(t, lbsMessage.DataStreamName, expectedMsgConsumer2)
			} else {
				if expectedMsgConsumer2 == "session1" {
					expectedMsgConsumer1 = "session1"
					require.Equal(t, lbsMessage.Info["key1"], "value1")
				} else {
					expectedMsgConsumer1 = "session2"
					require.Equal(t, lbsMessage.Info["key2"], "value2")
				}
			}
		}
	}

	consumer1.Close(ctx)
	_, ok := <-lbsChan1
	require.False(t, ok)
	consumer2.Close(ctx)
	_, ok = <-lbsChan2
	require.False(t, ok)
}

func TestKspNotifs(t *testing.T) {
	ctx := context.TODO()
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	res := redisClient.ConfigSet(ctx, types.NotifyKeyspaceEventsCmd, types.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	pubsub := redisClient.PSubscribe(ctx, types.ExpiredEventPattern)
	kspChan := pubsub.Channel(redisgo.WithChannelHealthCheckInterval(1*time.Second), redisgo.WithChannelSendTimeout(10*time.Minute))

	// now add a key and check if it times out
	redisClient.Set(ctx, "key1", "value1", time.Second)

	success := false

	for {
		select {
		case notif, ok := <-kspChan:
			require.True(t, ok)
			require.NotNil(t, notif)
			require.NotNil(t, notif.Payload)
			require.Equal(t, notif.Payload, "key1")
			success = true
		case <-time.After(time.Millisecond * 500):
		}

		if success {
			break
		}
	}

	require.True(t, success)
	require.NoError(t, pubsub.Close())
	// read from closed channel
	_, ok := <-kspChan
	require.False(t, ok)
	require.NoError(t, redisClient.Close())
}

func TestNewRedisStreamClientMainFlow(t *testing.T) {
	// Main flow:
	// there is one producer and two consumers: consumer1 and consumer2
	// producer adds messages and consumers consume.
	// consumer1 crashes
	// consumer2 is notified via ksp and it claims the stream

	// redis container
	redisContainer := setupSuite(t)

	// context based flow
	ctxWithCancel := context.TODO()
	consumer1Ctx, consumer1CancelFunc := context.WithCancel(ctxWithCancel)

	ctxWithCancel = context.TODO()
	consumer2Ctx, consumer2CancelFunc := context.WithCancel(ctxWithCancel)

	// create consumer1 client
	consumer1 := createConsumer("111", redisContainer)
	require.NotNil(t, consumer1)
	lbsChan1, kspChan1, err := consumer1.Init(consumer1Ctx)
	require.NoError(t, err)
	require.NotNil(t, lbsChan1)
	require.NotNil(t, kspChan1)

	// create consumer2 client
	consumer2 := createConsumer("222", redisContainer)
	require.NotNil(t, consumer2)
	lbsChan2, kspChan2, err := consumer2.Init(consumer2Ctx)
	require.NoError(t, err)
	require.NotNil(t, lbsChan2)
	require.NotNil(t, kspChan2)

	// add a new stream to lbsChan
	lbsMsg1, _ := json.Marshal(types.LBSMessage{
		DataStreamName: "session1",
		Info: map[string]interface{}{
			"key1": "value1",
		},
	})

	// add second stream to lbsChan
	lbsMsg2, _ := json.Marshal(types.LBSMessage{
		DataStreamName: "session2",
		Info: map[string]interface{}{
			"key2": "value2",
		},
	})

	producer := newRedisClient(redisContainer)
	_, err = producer.XAdd(context.Background(), &redisgo.XAddArgs{
		Stream: "consumer-input",
		Values: map[string]any{
			types.LBSInput: string(lbsMsg1),
		},
	}).Result()
	require.NoError(t, err)

	_, err = producer.XAdd(context.Background(), &redisgo.XAddArgs{
		Stream: "consumer-input",
		Values: map[string]any{
			types.LBSInput: string(lbsMsg2),
		},
	}).Result()
	require.NoError(t, err)

	// read from lbsChan
	streamsPickedup := 0
	consumer1Crashed := false

	i := 0

	for {

		if i == 10 {
			break
		}

		if streamsPickedup == 2 {
			// kill consumer1
			consumer1CancelFunc()
			consumer1Crashed = true
		}

		select {
		case notif, ok := <-kspChan2:
			require.True(t, consumer1Crashed)
			require.True(t, ok)
			require.NotNil(t, notif)
			require.NotNil(t, notif.Payload)
			require.Contains(t, notif.Payload, "session1")
			err = consumer2.Claim(consumer2Ctx, notif.Payload, "redis-consumer-222")
			require.NoError(t, err)
			res := producer.XInfoStreamFull(context.Background(), "consumer-input", 100)
			require.NotNil(t, res)
			require.NotNil(t, res.Val())
			grpInfo := res.Val().Groups
			require.NotEmpty(t, grpInfo)
			// there's only one group
			require.Len(t, grpInfo, 1)
			// there are two consumers
			require.Len(t, grpInfo[0].Consumers, 2)
			var c1, c2 *redisgo.XInfoStreamConsumer
			for _, c := range grpInfo[0].Consumers {
				if c.Name == "redis-consumer-111" {
					c1 = &c
				} else if c.Name == "redis-consumer-222" {
					c2 = &c
				}

				if c1 != nil && c2 != nil {
					break
				}
			}

			require.True(t, c1.ActiveTime.Before(c2.ActiveTime))
			require.True(t, c1.SeenTime.Before(c2.SeenTime))

		case msg, ok := <-lbsChan1:
			require.True(t, ok)
			require.NotNil(t, msg)
			var lbsMessage types.LBSMessage
			require.NoError(t, json.Unmarshal([]byte(msg.Values[types.LBSInput].(string)), &lbsMessage))
			require.NotNil(t, lbsMessage)
			require.Equal(t, "session1", lbsMessage.DataStreamName)
			require.Equal(t, "value1", lbsMessage.Info["key1"])
			streamsPickedup++
		case msg, ok := <-lbsChan2:
			require.True(t, ok)
			require.NotNil(t, msg)
			var lbsMessage types.LBSMessage
			require.NoError(t, json.Unmarshal([]byte(msg.Values[types.LBSInput].(string)), &lbsMessage))
			require.NotNil(t, lbsMessage)
			require.Equal(t, "session2", lbsMessage.DataStreamName)
			require.Equal(t, "value2", lbsMessage.Info["key2"])
			streamsPickedup++
		case <-time.After(time.Second):
		}

		i++
	}

	consumer2.Done(consumer2Ctx, "session1") // TODO: how will we get this dynamically?
	consumer2.Done(consumer2Ctx, "session2")

	consumer1.Close(consumer1Ctx)
	consumer2.Close(consumer2Ctx)

	// cancel the context
	consumer2CancelFunc()
	// consumer1 cancel should have been called
	// calling here to shut up the ctx leak error message
	consumer1CancelFunc()

	// kspchan must be closed automatically
	v, ok := <-kspChan1
	require.Nil(t, v)
	require.False(t, ok)
	v, ok = <-kspChan2
	require.Nil(t, v)
	require.False(t, ok)

	// lbs chan is also closed
	_, ok = <-lbsChan1
	require.False(t, ok)
}

func createConsumer(name string, redisContainer *redis.RedisContainer) types.RedisStreamClient {
	os.Setenv("POD_NAME", name)
	// create a new redis client
	return impl.NewRedisStreamClient(newRedisClient(redisContainer), time.Millisecond*20, "consumer")
}
