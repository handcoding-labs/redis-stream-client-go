package test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	redisgo "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/impl"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
)

// TestDoneInterruptsBlockedRead checks that Done interrupts the reader parked in XREADGROUP with
// CLIENT UNBLOCK instead of waiting for its block to end. The test servers allow CLIENT UNBLOCK.
func TestDoneInterruptsBlockedRead(t *testing.T) {
	ctx := context.TODO()
	redisContainer := setupSuite(t)

	consumer, _ := createConsumer("done-interrupt", redisContainer)
	opChan, err := consumer.Init(ctx)
	require.NoError(t, err)

	// let the reader park in XREADGROUP
	time.Sleep(500 * time.Millisecond)

	start := time.Now()
	require.NoError(t, consumer.Done(ctx))
	elapsed := time.Since(start)
	require.Less(t, elapsed, time.Second, "Done must interrupt the blocked read, not wait for it (took %v)", elapsed)

	_, ok := <-opChan
	require.False(t, ok)
}

// TestStoppedConsumerDoesNotSwallowNextMessage checks that once Done returns, the stopped consumer
// no longer has a read parked on the server: a message added afterwards goes to a live consumer
// instead of into the stopped one's pending list.
func TestStoppedConsumerDoesNotSwallowNextMessage(t *testing.T) {
	ctx := context.TODO()
	redisContainer := setupSuite(t)

	stopped, _ := createConsumer("swallow-stopped", redisContainer)
	stoppedChan, err := stopped.Init(ctx)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, stopped.Done(ctx))
	for range stoppedChan {
	}

	live, _ := createConsumer("swallow-live", redisContainer)
	liveChan, err := live.Init(ctx)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	addNStreamsToLBS(t, redisContainer, 1)

	select {
	case n := <-liveChan:
		require.Equal(t, notifs.StreamAdded, n.Type)
	case <-time.After(5 * time.Second):
		t.Fatal("the live consumer did not receive the message; the stopped consumer may have swallowed it")
	}

	require.NoError(t, live.Done(ctx))
}

// TestDoneFallsBackWhenClientUnblockIsDenied checks that a user who may not run CLIENT UNBLOCK still
// gets a working client whose Done returns within the bounded read block.
func TestDoneFallsBackWhenClientUnblockIsDenied(t *testing.T) {
	ctx := context.TODO()
	redisContainer := setupSuite(t)
	if redisContainer.isCluster() {
		t.Skip("ACL users are per node; the fallback path is the same in cluster mode")
	}

	admin := newRedisClient(redisContainer)
	defer func() { _ = admin.Close() }()
	// go-redis only authenticates when a password is set, so the user needs one
	const user, password = "rsc-no-unblock", "rsc-no-unblock-pass"
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">"+password, "~*", "&*", "+@all", "-client|unblock").Err())
	t.Cleanup(func() {
		if err := admin.Do(context.Background(), "ACL", "DELUSER", user).Err(); err != nil {
			t.Logf("deleting ACL user %s: %v", user, err)
		}
	})

	var logs []string
	capture := &captureHandler{output: &logs}

	_ = os.Setenv("POD_NAME", "done-fallback")
	rc := redisgo.NewClient(&redisgo.Options{Addr: redisContainer.addr, Username: user, Password: password})
	defer func() { _ = rc.Close() }()

	consumer, err := impl.NewRedisStreamClient(rc, "consumer",
		impl.WithForceConfigOverride(), impl.WithLogger(slog.New(capture)))
	require.NoError(t, err)

	opChan, err := consumer.Init(ctx)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- consumer.Done(ctx) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(configs.DefaultLBSReadBlock + 2*time.Second):
		t.Fatal("Done did not return within the bounded read block")
	}
	for range opChan {
	}

	fellBack := false
	for _, m := range capture.messages() {
		if strings.Contains(m, "CLIENT UNBLOCK is not permitted") {
			fellBack = true
		}
	}
	require.True(t, fellBack, "expected the client to log that it fell back to bounded reads")
}
