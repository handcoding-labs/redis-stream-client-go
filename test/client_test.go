package test

import (
	"bburli/redis-stream-client-go/impl"
	"bburli/redis-stream-client-go/types"
	"context"
	"encoding/json"
	"fmt"
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

	addNStreamsToLBS(t, redisContainer, 2)

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
				if lbsMessage.DataStreamName == "session0" {
					expectedMsgConsumer2 = "session1"
					require.Equal(t, lbsMessage.Info["key0"], "value0")
				} else {
					expectedMsgConsumer2 = "session0"
					require.Equal(t, lbsMessage.Info["key1"], "value1")
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

	consumer1.Done()
	consumer2.Done()

	_, ok := <-lbsChan1
	require.False(t, ok)
	_, ok = <-lbsChan2
	require.False(t, ok)
}

func TestClaimWorksOnlyOnce(t *testing.T) {
	ctxWCancel, cancelFunc := context.WithCancel(context.Background())
	ctxWOCancel := context.Background()

	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	res := redisClient.ConfigSet(ctxWOCancel, types.NotifyKeyspaceEventsCmd, types.KeyspacePatternForExpiredEvents)
	require.NoError(t, res.Err())

	// create consumer1 client
	consumer1 := createConsumer("111", redisContainer)
	require.NotNil(t, consumer1)
	lbsChan1, kspChan1, err := consumer1.Init(ctxWCancel)
	require.NoError(t, err)
	require.NotNil(t, lbsChan1)
	require.NotNil(t, kspChan1)

	// create consumer2 client
	consumer2 := createConsumer("222", redisContainer)
	require.NotNil(t, consumer2)
	lbsChan2, kspChan2, err := consumer2.Init(ctxWOCancel)
	require.NoError(t, err)
	require.NotNil(t, lbsChan2)
	require.NotNil(t, kspChan2)

	addNStreamsToLBS(t, redisContainer, 2)

	// create consumer3 client
	consumer3 := createConsumer("333", redisContainer)
	require.NotNil(t, consumer3)
	lbsChan3, kspChan3, err := consumer3.Init(ctxWOCancel)
	require.NoError(t, err)
	require.NotNil(t, lbsChan3)
	require.NotNil(t, kspChan3)

	// get streams owned by consumer1
	streamsOwnedByConsumer1 := consumer1.StreamsOwned()
	// kill consumer1
	cancelFunc()

	// consumer2 and consumer3 try to claim at the same time
	consumer2.Claim(ctxWOCancel, streamsOwnedByConsumer1[0]+":0")
	err = consumer3.Claim(ctxWOCancel, streamsOwnedByConsumer1[0]+":0")
	require.Error(t, err)
	require.Equal(t, err, fmt.Errorf("already claimed"))

	// Done is not called on consumer1 as it's crashed

	consumer2.Done()
	consumer3.Done()
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

func TestKspNotifsBulk(t *testing.T) {
	totalStreams := 1000
	totalConsumers := 10
	doomedConsumer1 := 3
	doomedConsumer2 := 7

	redisContainer := setupSuite(t)

	consumers := make(map[int]types.RedisStreamClient)
	cancelFuncs := make(map[int]context.CancelFunc)
	var kspChans []<-chan *redisgo.Message

	for i := range totalConsumers {
		ctxWithCancel := context.TODO()
		ctx, cancel := context.WithCancel(ctxWithCancel)

		// create consumer1 client
		consumer := createConsumer(fmt.Sprint(i), redisContainer)
		_, kspChan, err := consumer.Init(ctx)
		require.NoError(t, err)
		kspChans = append(kspChans, kspChan)

		consumers[i] = consumer
		cancelFuncs[i] = cancel
	}

	// add 1000 streams
	addNStreamsToLBS(t, redisContainer, totalStreams)

	time.Sleep(time.Second)

	// print all stream ownership
	preCancelTotal := 0
	for _, c := range consumers {
		fmt.Println(c.ID(), " has ", len(c.StreamsOwned()))
		preCancelTotal += len(c.StreamsOwned())
	}

	fmt.Println("before cancelling: ", preCancelTotal)

	// expected count of streams that will expire
	expiredStreamsCount := len(consumers[doomedConsumer1].StreamsOwned()) + len(consumers[doomedConsumer2].StreamsOwned())
	log.Println("expired streams = ", expiredStreamsCount)

	// start listening to kspChans and claim if we get a notification
	for i, ch := range kspChans {
		if i != 1 {
			go listenToKsp(t, ch, consumers, i, expiredStreamsCount)
		}
	}

	// give some time for all consumers to start listening
	time.Sleep(time.Second)

	// kill 2 consumers randomly
	cancelFuncs[doomedConsumer1]()
	cancelFuncs[doomedConsumer2]()

	// give time for expiry to kick in
	time.Sleep(5 * time.Second)

	// check claims and distribution
	// some streams are disconnected but all stream count for all consumers must still total 1000
	totalExpected := int64(totalStreams)
	totalActual := int64(0)

	rc := newRedisClient(redisContainer)
	res := rc.XInfoStreamFull(context.Background(), "consumer-input", 2000)
	require.NoError(t, res.Err())

	streamRes := res.Val()
	require.NotNil(t, streamRes)
	require.Len(t, streamRes.Groups, 1)

	grp := streamRes.Groups[0]
	probPelCount := 0
	for _, c := range grp.Consumers {
		if (c.Name == consumers[3].ID() || c.Name == consumers[7].ID()) && c.PelCount > 0 {
			probPelCount += int(c.PelCount)
		}

		totalActual += c.PelCount
	}

	fmt.Println("problematic pel count ", probPelCount)

	// see if any consumer has duplicate
	/*streamsKey := make(map[string]string)

	for i, c := range consumers {
		duplicates := 0
		// print for info
		//log.Println("consumer ", c.ID(), " has ", c.StreamsOwned())

		if !slices.Contains([]int{doomedConsumer1, doomedConsumer2}, i) {
			for _, s := range c.StreamsOwned() {
				if _, ok := streamsKey[s]; !ok {
					streamsKey[s] = c.ID()
				} else {
					duplicates++
					//log.Println("duplicate stream found: ", s, " existing owner ", streamsKey[s], " current owner ", c.ID())
				}
			}

			totalActual += int64(len(c.StreamsOwned()))
			fmt.Println("total after adding ", totalActual, " from consumer ", c.ID())
		}
	}

	fmt.Println("all unique streams : ", len(streamsKey))*/

	// overall the streams should be same
	// compare streams
	require.Equal(t, totalExpected, totalActual-int64(probPelCount))
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

	addNStreamsToLBS(t, redisContainer, 2)

	simpleRedisClient := newRedisClient(redisContainer)

	// read from lbsChan
	streamsPickedup := 0
	consumer1Crashed := false
	gotNotification := false

	i := 0

	for {

		if i == 10 {
			break
		}

		if streamsPickedup == 2 && !consumer1Crashed {
			// kill consumer1
			log.Println("killing consumer1")
			require.Len(t, consumer1.StreamsOwned(), 1)
			consumer1CancelFunc()
			consumer1Crashed = true
		}

		select {
		case notif, ok := <-kspChan2:
			gotNotification = true
			require.True(t, consumer1Crashed)
			require.True(t, ok)
			require.NotNil(t, notif)
			require.NotNil(t, notif.Payload)
			require.Contains(t, notif.Payload, "session")
			err = consumer2.Claim(consumer2Ctx, notif.Payload)
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

		case msg, ok := <-lbsChan1:
			if ok {
				require.NotNil(t, msg)
				var lbsMessage types.LBSMessage
				require.NoError(t, json.Unmarshal([]byte(msg.Values[types.LBSInput].(string)), &lbsMessage))
				require.NotNil(t, lbsMessage)
				require.Contains(t, lbsMessage.DataStreamName, "session")
				require.Contains(t, lbsMessage.Info["key0"], "value")
				streamsPickedup++
			}
		case msg, ok := <-lbsChan2:
			if ok {
				require.NotNil(t, msg)
				var lbsMessage types.LBSMessage
				require.NoError(t, json.Unmarshal([]byte(msg.Values[types.LBSInput].(string)), &lbsMessage))
				require.NotNil(t, lbsMessage)
				require.Contains(t, lbsMessage.DataStreamName, "session")
				require.Contains(t, lbsMessage.Info["key1"], "value")
				streamsPickedup++
			}
		case <-time.After(time.Second):
		}

		i++
	}

	require.True(t, gotNotification)
	consumer2.Done()
	// no Done is called on consumer1 because it crashed
	// either Done is called or context is cancelled

	// cancel the context
	consumer2CancelFunc()
	// consumer1 cancel should have been called
	// calling here to shut up the ctx leak error message
	consumer1CancelFunc()

	// kspchan may contain values as consumer1 crashes
	v, ok := <-kspChan2
	require.Nil(t, v)
	require.False(t, ok)
}

func addNStreamsToLBS(t *testing.T, redisContainer *redis.RedisContainer, n int) {
	stringify := func(name string, i int) string {
		return fmt.Sprintf("%s%d", name, i)
	}

	producer := newRedisClient(redisContainer)
	defer producer.Close()

	for i := range n {
		lbsMsg, _ := json.Marshal(types.LBSMessage{
			DataStreamName: stringify("session", i),
			Info: map[string]interface{}{
				stringify("key", i): stringify("value", i),
			},
		})

		_, err := producer.XAdd(context.Background(), &redisgo.XAddArgs{
			Stream: "consumer-input",
			Values: map[string]any{
				types.LBSInput: string(lbsMsg),
			},
		}).Result()
		require.NoError(t, err)
	}
}

func createConsumer(name string, redisContainer *redis.RedisContainer) types.RedisStreamClient {
	_ = os.Setenv("POD_NAME", name)
	// create a new redis client
	return impl.NewRedisStreamClient(newRedisClient(redisContainer), time.Second, "consumer")
}

func listenToKsp(t *testing.T, kspChan <-chan *redisgo.Message, consumers map[int]types.RedisStreamClient, i int, expiredStreamCount int) {
	totalClaimed := 0

	for {

		if totalClaimed == expiredStreamCount {
			break
		}

		select {
		case notif, ok := <-kspChan:
			require.True(t, ok)
			require.NotNil(t, notif)
			require.NotNil(t, notif.Payload)
			require.Contains(t, notif.Payload, "session")
			preClaimCount := len(consumers[i].StreamsOwned())
			err := consumers[i].Claim(context.Background(), notif.Payload)
			if err != nil {
				continue
			}
			postClaimCount := len(consumers[i].StreamsOwned())
			totalClaimed += postClaimCount - preClaimCount
		default:
		}
	}
}
