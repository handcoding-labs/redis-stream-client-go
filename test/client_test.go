package test

import (
	"bburli/redis-stream-client/source/impl"
	"bburli/redis-stream-client/source/types"
	"context"
	"encoding/json"
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

func TestNewRedisStreamClient(t *testing.T) {
	redisContainer := setupSuite(t)

	os.Setenv("POD_NAME", "111")
	// create a new redis client
	consumer := impl.NewRedisStreamClient(newRedisClient(redisContainer), time.Millisecond*10, "consumer")
	require.NotNil(t, consumer)
	lbsChan, kspChan, err := consumer.Init(context.Background())
	require.NoError(t, err)
	require.NotNil(t, lbsChan)
	require.NotNil(t, kspChan)

	// add a new stream to lbsChan
	lbsMsg, _ := json.Marshal(types.LBSMessage{
		DataStreamName: "session1",
		Info: map[string]interface{}{
			"key1": "value1",
		},
	})

	producer := newRedisClient(redisContainer)
	_, err = producer.XAdd(context.Background(), &redisgo.XAddArgs{
		Stream: "consumer-input",
		Values: map[string]any{
			types.LBSInput: string(lbsMsg),
		},
	}).Result()
	require.NoError(t, err)

	// read from lbsChan
	i := 0
	success := false

	for {

		if i == 10 {
			break
		}

		select {
		case msg := <-lbsChan:
			require.NotNil(t, msg)
			require.Equal(t, "session1", msg.Values["lbs"].(types.LBSMessage).DataStreamName)
			require.Equal(t, "value1", msg.Values["lbs"].(types.LBSMessage).Info["key1"])
			success = true
		case <-time.After(time.Second):
		}

		i++
	}

	require.True(t, success)
}
