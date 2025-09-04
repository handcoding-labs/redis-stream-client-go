package test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	redisgo "github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/impl"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

func newRedisClient(redisContainer *redis.RedisContainer) redisgo.UniversalClient {
	connString, err := redisContainer.ConnectionString(context.Background())
	if err != nil {
		panic(err)
	}

	connString = connString[8:] // remove redis:// prefix*/

	return redisgo.NewUniversalClient(&redisgo.UniversalOptions{
		Addrs: []string{connString}, // hard code to "localhost:6379" if you're testing locally and a container is up
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
	res := redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	// create consumer1 client
	consumer1 := createConsumer("111", redisContainer)
	require.NotNil(t, consumer1)
	opChan1, err := consumer1.Init(ctx)
	require.NoError(t, err)
	require.NotNil(t, opChan1)

	// create consumer2 client
	consumer2 := createConsumer("222", redisContainer)
	require.NotNil(t, consumer2)
	opChan2, err := consumer2.Init(ctx)
	require.NoError(t, err)
	require.NotNil(t, opChan2)

	addNStreamsToLBS(t, redisContainer, 2)

	// load balanced stream distributes messages to different consumers in a load balanced way
	// so we keep track of which stream was given to consumer1 so that we can check if consumer2 gets another one
	var expectedMsgConsumer2 string
	var expectedMsgConsumer1 string

	for i := 0; i < 2; i++ {
		log.Println("iteration: ", i)
		select {
		case msg, ok := <-opChan1:
			switch msg.Type {
			case notifs.StreamAdded:
				require.True(t, ok)
				require.NotNil(t, msg)
				var lbsMessage notifs.LBSMessage
				require.NoError(t, json.Unmarshal([]byte(msg.Payload.(string)), &lbsMessage))
				require.NotNil(t, lbsMessage)

				if expectedMsgConsumer1 != "" {
					require.Equal(t, lbsMessage.DataStreamName, expectedMsgConsumer1)
				} else {
					if lbsMessage.DataStreamName == "session0" {
						expectedMsgConsumer2 = "session1"
						require.Equal(t, lbsMessage.Info["key0"], "value0")
					} else {
						expectedMsgConsumer2 = "session0"
						require.Equal(t, lbsMessage.Info["key1"], "value1")
					}
				}
			}
		case msg, ok := <-opChan2:
			switch msg.Type {
			case notifs.StreamAdded:
				require.True(t, ok)
				require.NotNil(t, msg)
				var lbsMessage notifs.LBSMessage
				require.NoError(t, json.Unmarshal([]byte(msg.Payload.(string)), &lbsMessage))
				require.NotNil(t, lbsMessage)
				if expectedMsgConsumer2 != "" {
					require.Equal(t, lbsMessage.DataStreamName, expectedMsgConsumer2)
				} else {
					if lbsMessage.DataStreamName == "session0" {
						expectedMsgConsumer1 = "session1"
						require.Equal(t, lbsMessage.Info["key0"], "value0")
					} else {
						expectedMsgConsumer1 = "session0"
						require.Equal(t, lbsMessage.Info["key1"], "value1")
					}
				}
			}
		}
	}

	err = consumer1.Done()
	require.NoError(t, err)
	err = consumer2.Done()
	require.NoError(t, err)

	_, ok := <-opChan1
	require.False(t, ok)

	_, ok = <-opChan2
	require.False(t, ok)
}

func TestLBSRecovery(t *testing.T) {
	ctxWCancel, cancelFunc := context.WithCancel(context.Background())
	ctx := context.TODO()
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	res := redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	consumer := createConsumer("111", redisContainer)
	opChan, err := consumer.Init(ctxWCancel)
	require.NoError(t, err)
	require.NotNil(t, opChan)

	addNStreamsToLBS(t, redisContainer, 1)

	time.Sleep(1 * time.Second)

	// kill consumer don't ack the message
	cancelFunc()

	// restart consumer
	consumer = createConsumer("111", redisContainer)
	opChan, err = consumer.Init(ctx)
	require.NoError(t, err)
	require.NotNil(t, opChan)

	select {
	case msg, ok := <-opChan:
		if ok {
			t.Log("received message after recovery: ", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("did not receive message after recovery")
	}
	err = consumer.Done()
	require.NoError(t, err)
	_, ok := <-opChan
	require.False(t, ok)
}

func TestClaimWorksOnlyOnce(t *testing.T) {
	ctxWCancel, cancelFunc := context.WithCancel(context.Background())
	ctxWOCancel := context.Background()

	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	res := redisClient.ConfigSet(ctxWOCancel, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	// create consumer1 client
	consumer1 := createConsumer("111", redisContainer)
	require.NotNil(t, consumer1)
	opChan1, err := consumer1.Init(ctxWCancel)
	require.NoError(t, err)
	require.NotNil(t, opChan1)

	// create consumer2 client
	consumer2 := createConsumer("222", redisContainer)
	require.NotNil(t, consumer2)
	opChan2, err := consumer2.Init(ctxWOCancel)
	require.NoError(t, err)
	require.NotNil(t, opChan2)

	addNStreamsToLBS(t, redisContainer, 1)

	// create consumer3 client
	consumer3 := createConsumer("333", redisContainer)
	require.NotNil(t, consumer3)
	opChan3, err := consumer3.Init(ctxWOCancel)
	require.NoError(t, err)
	require.NotNil(t, opChan3)

	// kill consumer1
	cancelFunc()

	// print out who the xinfo for stream
	xinfoRes := redisClient.XInfoStreamFull(ctxWOCancel, "consumer-input", 1)
	require.NoError(t, xinfoRes.Err())
	streamRes := xinfoRes.Val()
	require.NotNil(t, streamRes)
	require.Len(t, streamRes.Groups, 1)
	grp := streamRes.Groups[0]
	for _, c := range grp.Consumers {
		log.Println("consumer: ", c.Name, c.PelCount, c.Pending)
		for _, m := range c.Pending {
			log.Println("message: ", m.ID, m.DeliveryTime)
		}
	}

	time.Sleep(3 * time.Second)

	// consumer2 and consumer3 try to claim at the same time
	// Get the actual message ID from the pending messages to claim
	var actualMutexKey string
	for _, c := range grp.Consumers {
		if c.Name == "redis-consumer-111" && len(c.Pending) > 0 {
			actualMutexKey = fmt.Sprintf("session0:%s", c.Pending[0].ID)
			break
		}
	}
	require.NotEmpty(t, actualMutexKey, "Could not find pending message for consumer1")

	err = consumer2.Claim(ctxWOCancel, actualMutexKey)
	require.NoError(t, err)
	err = consumer3.Claim(ctxWOCancel, actualMutexKey)
	require.Error(t, err)
	require.Equal(t, err, fmt.Errorf("already claimed"))

	// Done is not called on consumer1 as it's crashed
	err = consumer2.Done()
	require.NoError(t, err)
	err = consumer3.Done()
	require.NoError(t, err)
}

func TestBlockingRead(t *testing.T) {
	ctx := context.TODO()
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	res := redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	consumer := createConsumer("111", redisContainer)
	opChan, err := consumer.Init(ctx)
	require.NoError(t, err)

	select {
	case <-opChan:
		t.Fatalf("unexpected message before add")
	case <-time.After(500 * time.Millisecond):
	}

	addNStreamsToLBS(t, redisContainer, 1)

	var got bool
	select {
	case msg := <-opChan:
		require.Equal(t, notifs.StreamAdded, msg.Type)
		got = true
	case <-time.After(2 * time.Second):
		t.Fatalf("did not receive message")
	}

	require.True(t, got)
	err = consumer.Done()
	require.NoError(t, err)
	_, ok := <-opChan
	require.False(t, ok)
}

func TestKspNotifs(t *testing.T) {
	ctx := context.TODO()
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	res := redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	pubsub := redisClient.PSubscribe(ctx, configs.ExpiredEventPattern)
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

func TestKspNotifsBulk(t *testing.T) {
	// this is kept 3000 because go test suite times out at 30s and this is the number of streams that we can process
	// to test higher numbers run locally by increasing timeout : go test -timeout=600s ...
	totalStreams := 3000
	totalConsumers := totalStreams / 200 // having low number of consumers will create lag

	redisContainer := setupSuite(t)
	// client for testing and assertion purposes
	rc := newRedisClient(redisContainer)

	consumers := make(map[int]types.RedisStreamClient)
	cancelFuncs := make(map[int]context.CancelFunc)
	var outputChans []<-chan notifs.RecoverableRedisNotification[any]

	for i := 0; i < totalConsumers; i++ {
		ctxWithCancel := context.TODO()
		ctx, cancel := context.WithCancel(ctxWithCancel)

		// create consumer1 client
		consumer := createConsumer(fmt.Sprint(i), redisContainer)
		opChan, err := consumer.Init(ctx)
		require.NoError(t, err)
		outputChans = append(outputChans, opChan)

		consumers[i] = consumer
		cancelFuncs[i] = cancel
	}

	addNStreamsToLBS(t, redisContainer, totalStreams)

	// start listening to kspChans and claim if we get a notification
	for i, ch := range outputChans {
		if i != 3 && i != 7 {
			go listenToKsp(t, ch, consumers, i)
		}
	}

	// kill 2 consumers randomly
	cancelFuncs[3]()
	cancelFuncs[7]()

	// check claims and distribution
	// some streams are disconnected but all stream count for all consumers must still total 1000
	totalExpected := int64(totalStreams)
	done := false

	log.Println("start checking ", time.Now())
	for {
		if done {
			break
		}

		res := rc.XInfoStreamFull(context.Background(), "consumer-input", totalStreams)
		require.NoError(t, res.Err())

		streamRes := res.Val()
		require.NotNil(t, streamRes)
		require.Len(t, streamRes.Groups, 1)

		grp := streamRes.Groups[0]
		totalActual := int64(0)
		for _, c := range grp.Consumers {
			if c.Name == consumers[3].ID() || c.Name == consumers[7].ID() {
				totalActual += c.PelCount
			}
		}

		if totalActual == 0 {
			done = true
		}

		time.Sleep(time.Second)
	}

	log.Println("check complete", time.Now())

	totalActual := int64(0)

	res := rc.XInfoStreamFull(context.Background(), "consumer-input", totalStreams)
	require.NoError(t, res.Err())

	streamRes := res.Val()
	require.NotNil(t, streamRes)
	require.Len(t, streamRes.Groups, 1)

	grp := streamRes.Groups[0]
	for _, c := range grp.Consumers {
		if c.Name != consumers[3].ID() && c.Name != consumers[7].ID() {
			totalActual += c.PelCount
		}
	}

	require.Equal(t, totalExpected, totalActual)
}

func TestMainFlow(t *testing.T) {
	// Main flow:
	// there is one producer and two consumers: consumer1 and consumer2
	// producer adds messages and consumers consume.
	// consumer1 crashes
	// consumer2 is notified via ksp and it claims the stream

	// redis container
	// defer goleak.VerifyNone(t)
	redisContainer := setupSuite(t)

	// context based flow
	ctxWithCancel := context.TODO()
	consumer1Ctx, consumer1CancelFunc := context.WithCancel(ctxWithCancel)

	ctxWithCancel = context.TODO()
	consumer2Ctx, consumer2CancelFunc := context.WithCancel(ctxWithCancel)

	// create consumer1 client
	consumer1 := createConsumer("111", redisContainer)
	require.NotNil(t, consumer1)
	opChan1, err := consumer1.Init(consumer1Ctx)
	require.NoError(t, err)
	require.NotNil(t, opChan1)

	// create consumer2 client
	consumer2 := createConsumer("222", redisContainer)
	require.NotNil(t, consumer2)
	opChan2, err := consumer2.Init(consumer2Ctx)
	require.NoError(t, err)
	require.NotNil(t, opChan2)

	addNStreamsToLBS(t, redisContainer, 2)

	simpleRedisClient := newRedisClient(redisContainer)

	// read from lbsChan
	streamsPickedup := 0
	consumer1Crashed := false
	gotNotification := false
	readingSuccess := false

	i := 0
	for {
		if streamsPickedup == 2 {
			readingSuccess = true
			break
		}

		if i == 10 {
			break
		}

		select {
		case msg, ok := <-opChan1:
			if ok {
				switch msg.Type {
				case notifs.StreamAdded:
					require.True(t, ok)
					require.NotNil(t, msg)
					var lbsMessage notifs.LBSMessage
					require.NoError(t, json.Unmarshal([]byte(msg.Payload.(string)), &lbsMessage))
					require.NotNil(t, lbsMessage)
					require.Contains(t, lbsMessage.DataStreamName, "session")
					require.Contains(t, lbsMessage.Info["key0"], "value")
					streamsPickedup++
				}
			}
		case msg, ok := <-opChan2:
			if ok {
				switch msg.Type {
				case notifs.StreamAdded:
					require.True(t, ok)
					require.NotNil(t, msg)
					var lbsMessage notifs.LBSMessage
					require.NoError(t, json.Unmarshal([]byte(msg.Payload.(string)), &lbsMessage))
					require.NotNil(t, lbsMessage)
					require.Contains(t, lbsMessage.DataStreamName, "session")
					require.Contains(t, lbsMessage.Info["key1"], "value")
					streamsPickedup++
				}
			}
		case <-time.After(time.Second):
		}

		i++
	}

	require.True(t, readingSuccess)

	// kill consumer1
	log.Println("killing consumer1")
	consumer1CancelFunc()
	consumer1Crashed = true

	// check that no StreamDisowned notification is emitted on normal shutdown
	streamDisowned := false
	select {
	case msg := <-opChan1:
		if msg.Type == notifs.StreamDisowned {
			streamDisowned = true
		}
	case <-time.After(time.Second):
	}

	require.False(t, streamDisowned)
	claimSuccess := false
	i = 0
	streamsPickedup = 0
	for {
		log.Println("iteration ", i)

		if i == 10 || claimSuccess {
			break
		}

		select {
		case notif, ok := <-opChan2:
			switch notif.Type {
			case notifs.StreamExpired:
				gotNotification = true
				require.True(t, consumer1Crashed)
				require.True(t, ok)
				require.NotNil(t, notif)
				require.Contains(t, notif.Payload, "session")
				err = consumer2.Claim(consumer2Ctx, notif.Payload.(string))
				require.NoError(t, err)
				res := simpleRedisClient.XInfoStreamFull(context.Background(), "consumer-input", 100)
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

				claimSuccess = true
			}
		case <-time.After(time.Second):
		}

		i++
	}

	// test if 10 seconds were not spent because we don't expect it to take 10 seconds to claim
	// so its a failure
	require.Less(t, i, 10)

	require.True(t, gotNotification)
	err = consumer2.Done()
	require.NoError(t, err)
	// no Done is called on consumer1 because it crashed
	// either Done is called or context is canceled

	// cancel the context
	consumer2CancelFunc()
	// consumer1 cancel should have been called
	// calling here to shut up the ctx leak error message
	consumer1CancelFunc()

	// check if outputChan is closed
	_, ok := <-opChan2
	require.False(t, ok)
}

func addNStreamsToLBS(t *testing.T, redisContainer *redis.RedisContainer, n int) {
	stringify := func(name string, i int) string {
		return fmt.Sprintf("%s%d", name, i)
	}

	producer := newRedisClient(redisContainer)
	defer producer.Close()

	for i := 0; i < n; i++ {
		lbsMsg, err := json.Marshal(notifs.LBSMessage{
			DataStreamName: stringify("session", i),
			Info: map[string]interface{}{
				stringify("key", i): stringify("value", i),
			},
		})
		require.NoError(t, err)

		_, err = producer.XAdd(context.Background(), &redisgo.XAddArgs{
			Stream: "consumer-input",
			Values: map[string]any{
				configs.LBSInput: string(lbsMsg),
			},
		}).Result()
		require.NoError(t, err, "failed to add stream %d", i)
	}
}

func createConsumer(name string, redisContainer *redis.RedisContainer) types.RedisStreamClient {
	_ = os.Setenv("POD_NAME", name)
	// create a new redis client
	return impl.NewRedisStreamClient(newRedisClient(redisContainer), "consumer")
}

func listenToKsp(t *testing.T, outputChan <-chan notifs.RecoverableRedisNotification[any], consumers map[int]types.RedisStreamClient, i int) {
	for notif := range outputChan {
		switch notif.Type {
		case notifs.StreamExpired:
			require.NotNil(t, notif)
			streamThatExpired, ok := notif.Payload.(string)
			require.True(t, ok)
			require.Contains(t, streamThatExpired, "session")
			err := consumers[i].Claim(context.Background(), streamThatExpired)
			if err != nil {
				continue
			}
		}
	}
}
