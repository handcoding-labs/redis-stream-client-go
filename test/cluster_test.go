package test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	redisgo "github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/impl"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"

	"github.com/stretchr/testify/require"
)

// waitForStreamAdded reads from a notification channel until it sees a StreamAdded for the given
// data stream name (or any, if name is empty), or fails after the timeout.
func waitForStreamAdded(t *testing.T, ch <-chan notifs.RecoverableRedisNotification, name string, timeout time.Duration) notifs.RecoverableRedisNotification {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-ch:
			require.True(t, ok, "notification channel closed unexpectedly")
			if msg.Type == notifs.StreamAdded && (name == "" || msg.Payload.DataStreamName == name) {
				return msg
			}
		case <-deadline:
			t.Fatalf("timed out waiting for StreamAdded (name=%q)", name)
		}
	}
}

// TestMutexCheckPreventsDuplicateProcessingOfSlowConsumers covers issue #111.
//
// A consumer holds a stream's lock and keeps it alive while processing slowly (it never calls
// DoneStream). Its LBS message stays pending and its idle time grows past MinIdleTime. A second
// consumer's reconciliation scan must observe that the lock key still exists and SKIP re-queuing,
// so the slow consumer's work is never duplicated.
func TestMutexCheckPreventsDuplicateProcessingOfSlowConsumers(t *testing.T) {
	ctx := context.Background()
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	require.NoError(t, redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents).Err())

	// consumer1 holds the stream (alive, slow). Fast recovery so its own scan also runs.
	consumer1, rec1 := createConsumerWithRecovery("111", redisContainer)
	opChan1, err := consumer1.Init(ctx)
	require.NoError(t, err)

	addNStreamsToLBS(t, redisContainer, 1)

	held := waitForStreamAdded(t, opChan1, "session0", 5*time.Second)
	require.Equal(t, "session0", held.Payload.DataStreamName)

	// consumer2 joins and runs its reconciliation scan. It must NOT steal the stream.
	consumer2, rec2 := createConsumerWithRecovery("222", redisContainer)
	opChan2, err := consumer2.Init(ctx)
	require.NoError(t, err)

	// Wait past MinIdleTime so the pending message is idle-eligible and is inspected by scans.
	require.Eventually(t, func() bool {
		return rec2.MutexAliveSkipCount() >= 1
	}, 10*time.Second, 200*time.Millisecond, "consumer2 should skip the still-locked stream")

	// consumer2 must not have received a StreamAdded for the still-held stream.
	select {
	case msg, ok := <-opChan2:
		if ok && msg.Type == notifs.StreamAdded {
			t.Fatalf("consumer2 stole stream %q from a live consumer", msg.Payload.DataStreamName)
		}
	case <-time.After(2 * time.Second):
		// expected: no StreamAdded for consumer2
	}

	require.Equal(t, 0, rec2.ReQueueCount(), "no re-queue should happen while the lock is held")
	require.Equal(t, 1, rec1.StreamProcessingStartCount(), "consumer1 still owns exactly one stream")
	require.Equal(t, 0, rec1.StreamProcessingEndCount(), "consumer1 has not finished the stream")

	require.NoError(t, consumer1.Done(ctx))
	require.NoError(t, consumer2.Done(ctx))
}

// TestXAckFirstDeduplicationAcrossConcurrentRecoverers covers issue #112.
//
// When a consumer dies, multiple other consumers may try to recover the same pending message
// concurrently. The XACK-first ordering guarantees that exactly one of them re-queues the task; the
// others observe acked==0 and return ErrAlreadyClaimed.
//
// The test drives this deterministically by invoking Claim on the *same* stranded entry from two
// consumers concurrently. Claim performs a single re-queue attempt (no scan loop), so the assertion
// isolates the XACK-first dedup guarantee from the at-least-once re-queue churn a periodic scan can
// introduce (a re-queued message being re-queued again before it is locked), which would otherwise
// make a raw re-queue count non-deterministic.
func TestXAckFirstDeduplicationAcrossConcurrentRecoverers(t *testing.T) {
	ctx := context.Background()
	victimCtx, killVictim := context.WithCancel(context.Background())
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	require.NoError(t, redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents).Err())

	// victim consumer takes the stream, then dies. Default config everywhere so no periodic scan
	// interferes — recovery is driven solely by the explicit concurrent Claim calls below.
	victim, _ := createConsumer("000", redisContainer)
	victimChan, err := victim.Init(victimCtx)
	require.NoError(t, err)

	addNStreamsToLBS(t, redisContainer, 1)
	got := waitForStreamAdded(t, victimChan, "session0", 5*time.Second)
	require.Equal(t, "session0", got.Payload.DataStreamName)

	// two recoverers come up
	rec1Client, rec1 := createConsumer("111", redisContainer)
	op1, err := rec1Client.Init(ctx)
	require.NoError(t, err)

	rec2Client, rec2 := createConsumer("222", redisContainer)
	op2, err := rec2Client.Init(ctx)
	require.NoError(t, err)

	// drain recoverers' channels so they don't block
	go func() {
		for range op1 {
		}
	}()
	go func() {
		for range op2 {
		}
	}()

	// kill the victim and wait for its lock to expire (TTL == heartbeat interval), so the recoverers
	// observe the owner as dead and the entry becomes recoverable.
	killVictim()
	time.Sleep(4 * time.Second)

	// resolve the stranded pending entry id (still owned by the dead victim in the group PEL)
	pending, err := redisClient.XPendingExt(ctx, &redisgo.XPendingExtArgs{
		Stream: "consumer-input",
		Group:  "consumer-group",
		Start:  "-",
		End:    "+",
		Count:  10,
	}).Result()
	require.NoError(t, err)
	require.NotEmpty(t, pending, "the victim's message should still be pending")
	lbsInfo := notifs.LBSInfo{DataStreamName: "session0", IDInLBS: pending[0].ID}

	// both recoverers Claim the same entry concurrently
	var wg sync.WaitGroup
	claimErrs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); claimErrs[0] = rec1Client.Claim(ctx, lbsInfo) }()
	go func() { defer wg.Done(); claimErrs[1] = rec2Client.Claim(ctx, lbsInfo) }()
	wg.Wait()

	won := 0
	for _, e := range claimErrs {
		if e == nil {
			won++
		} else {
			require.ErrorIs(t, e, errs.ErrAlreadyClaimed, "the losing Claim must report ErrAlreadyClaimed")
		}
	}
	require.Equal(t, 1, won, "exactly one Claim must win the XACK race")
	require.Equal(t, 1, rec1.ReQueueCount()+rec2.ReQueueCount(),
		"exactly one recoverer re-queued the entry (XACK-first dedup)")

	require.NoError(t, rec1Client.Done(ctx))
	require.NoError(t, rec2Client.Done(ctx))
}

// TestMultiShardRecoveryViaPeriodicScan covers issue #114.
//
// It demonstrates that recovery works purely through the periodic reconciliation scan, with no
// reliance on keyspace notifications / Claim. This is the cluster-safe path: a different consumer
// (that never handles StreamExpired) recovers a dead consumer's work via the scan alone.
func TestMultiShardRecoveryViaPeriodicScan(t *testing.T) {
	ctx := context.Background()
	deadCtx, killConsumer := context.WithCancel(context.Background())
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	require.NoError(t, redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents).Err())

	consumer1, _ := createConsumerWithRecovery("111", redisContainer)
	op1, err := consumer1.Init(deadCtx)
	require.NoError(t, err)

	addNStreamsToLBS(t, redisContainer, 1)
	got := waitForStreamAdded(t, op1, "session0", 5*time.Second)
	require.Equal(t, "session0", got.Payload.DataStreamName)

	// kill consumer1; its lock expires shortly after.
	killConsumer()

	// consumer2 never wires Claim/StreamExpired handling; recovery must come from the scan.
	consumer2, rec2 := createConsumerWithRecovery("222", redisContainer)
	op2, err := consumer2.Init(ctx)
	require.NoError(t, err)

	recovered := waitForStreamAdded(t, op2, "session0", 15*time.Second)
	require.Equal(t, "session0", recovered.Payload.DataStreamName)
	require.GreaterOrEqual(t, rec2.ReQueueCount(), 1, "stream should be recovered via the periodic scan")

	require.NoError(t, consumer2.Done(ctx))
}

// TestRetryCountAndDLQRouting covers issue #106: messages that exceed MaxRetries are routed to the
// configured DLQ stream with a retry count and reason, and removed from the LBS.
func TestRetryCountAndDLQRouting(t *testing.T) {
	ctx := context.Background()
	redisContainer := setupSuite(t)

	redisClient := newRedisClient(redisContainer)
	require.NoError(t, redisClient.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents).Err())

	const dlqStream = "consumer-dlq"

	// MaxRetries == 0 means the first recovery attempt already exceeds the limit and goes to DLQ.
	cfg := fastRecoveryConfig()
	cfg.MaxRetries = 0
	cfg.DLQStream = dlqStream

	_ = os.Setenv("POD_NAME", "111")
	rec := &testMetricsRecorder{}
	client, err := impl.NewRedisStreamClient(
		newRedisClient(redisContainer),
		"consumer",
		impl.WithForceConfigOverride(),
		impl.WithRecoveryConfig(cfg),
		impl.WithMetricsRecorder(rec),
	)
	require.NoError(t, err)

	// Create the group, then add an LBS message and leave it pending (delivered to an absent
	// consumer) with no lock key -> recovery sees a dead owner and, with MaxRetries==0, DLQs it.
	require.NoError(t, redisClient.XGroupCreateMkStream(ctx, "consumer-input", "consumer-group", "$").Err())
	require.NoError(t, redisClient.XAdd(ctx, &redisgo.XAddArgs{
		Stream: "consumer-input",
		Values: map[string]any{configs.LBSInput: `{"DataStreamName":"session0","Info":{"k":"v"}}`},
	}).Err())
	read := redisClient.XReadGroup(ctx, &redisgo.XReadGroupArgs{
		Group:    "consumer-group",
		Consumer: "ghost",
		Streams:  []string{"consumer-input", ">"},
		Count:    1,
		Block:    time.Second,
	})
	require.NoError(t, read.Err())

	opChan, err := client.Init(ctx)
	require.NoError(t, err)
	go func() {
		for range opChan {
		}
	}()

	require.Eventually(t, func() bool {
		return redisClient.XLen(ctx, dlqStream).Val() == 1
	}, 15*time.Second, 200*time.Millisecond, "message should be routed to the DLQ")

	// validate DLQ entry carries the retry count + reason and the original payload
	dlq := redisClient.XRange(ctx, dlqStream, "-", "+").Val()
	require.Len(t, dlq, 1)
	values := dlq[0].Values
	require.Equal(t, configs.DLQReasonMaxRetries, values[configs.DLQReasonField])
	require.Equal(t, "1", values[configs.RetryCountField], "retry count is incremented to 1 before DLQ")
	require.Contains(t, values[configs.LBSInput], "session0")

	// the original LBS entry must be gone and nothing left pending
	require.Equal(t, int64(0), redisClient.XLen(ctx, "consumer-input").Val(), "original LBS entry removed")
	require.GreaterOrEqual(t, rec.DLQRoutingCount(), 1, "DLQ routing metric recorded")

	require.NoError(t, client.Done(ctx))
}

// TestRetryCountEscalatesAcrossReQueuesToDLQ covers the re-queue "churn" path: a stranded message
// that is recovered repeatedly without ever being processed to completion has its _retry_count
// incremented on each re-queue, and is routed to the DLQ once the count would exceed MaxRetries.
//
// It exercises the exact reQueue primitive the periodic scan uses, but drives it deterministically
// through Claim (one re-queue attempt per call) rather than racing scan passes — the escalation is
// the behavior under test, and scan-timed churn is inherently non-deterministic. The client is not
// Init'd, so no background reader/scan competes for the re-queued messages; a throwaway "ghost"
// consumer makes each re-queued entry pending (with no lock) so the next Claim sees a dead owner.
func TestRetryCountEscalatesAcrossReQueuesToDLQ(t *testing.T) {
	ctx := context.Background()
	redisContainer := setupSuite(t)
	rc := newRedisClient(redisContainer)
	require.NoError(t, rc.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, configs.KeyspacePatternForExpiredEvents).Err())

	const (
		dlqStream  = "consumer-dlq"
		maxRetries = 2
	)
	cfg := fastRecoveryConfig()
	cfg.MaxRetries = maxRetries
	cfg.DLQStream = dlqStream

	_ = os.Setenv("POD_NAME", "recoverer")
	rec := &testMetricsRecorder{}
	client, err := impl.NewRedisStreamClient(
		newRedisClient(redisContainer),
		"consumer",
		impl.WithForceConfigOverride(),
		impl.WithRecoveryConfig(cfg),
		impl.WithMetricsRecorder(rec),
	)
	require.NoError(t, err)

	require.NoError(t, rc.XGroupCreateMkStream(ctx, "consumer-input", "consumer-group", "$").Err())

	// ghostRead makes the newest LBS entry pending under a throwaway consumer (no lock acquired) and
	// returns it, so the next Claim observes a dead owner and re-queues it.
	ghostRead := func() redisgo.XMessage {
		res := rc.XReadGroup(ctx, &redisgo.XReadGroupArgs{
			Group:    "consumer-group",
			Consumer: "ghost",
			Streams:  []string{"consumer-input", ">"},
			Count:    1,
			Block:    2 * time.Second,
		})
		require.NoError(t, res.Err())
		require.Len(t, res.Val(), 1)
		require.Len(t, res.Val()[0].Messages, 1)
		return res.Val()[0].Messages[0]
	}
	claim := func(id string) error {
		return client.Claim(ctx, notifs.LBSInfo{DataStreamName: "session0", IDInLBS: id})
	}

	// seed the first entry (retry_count absent == 0)
	require.NoError(t, rc.XAdd(ctx, &redisgo.XAddArgs{
		Stream: "consumer-input",
		Values: map[string]any{configs.LBSInput: `{"DataStreamName":"session0","Info":{"k":"v"}}`},
	}).Err())
	cur := ghostRead()

	// each re-queue increments _retry_count: 0 -> 1 -> 2, up to MaxRetries
	for want := 1; want <= maxRetries; want++ {
		require.NoError(t, claim(cur.ID), "re-queue attempt %d should succeed", want)
		cur = ghostRead()
		require.Equal(t, strconv.Itoa(want), cur.Values[configs.RetryCountField],
			"retry count after re-queue %d", want)
	}
	require.Equal(t, maxRetries, rec.ReQueueCount(), "one successful re-queue per attempt")

	// the next recovery would push retry_count past MaxRetries -> route to DLQ instead
	require.NoError(t, claim(cur.ID), "exhausting attempt should succeed (routes to DLQ)")

	dlq := rc.XRange(ctx, dlqStream, "-", "+").Val()
	require.Len(t, dlq, 1, "message should land in the DLQ after exhausting retries")
	require.Equal(t, strconv.Itoa(maxRetries+1), dlq[0].Values[configs.RetryCountField])
	require.Equal(t, configs.DLQReasonMaxRetries, dlq[0].Values[configs.DLQReasonField])
	require.Contains(t, dlq[0].Values[configs.LBSInput], "session0")
	require.Equal(t, int64(0), rc.XLen(ctx, "consumer-input").Val(), "nothing left in the LBS")
	require.GreaterOrEqual(t, rec.DLQRoutingCount(), 1, "DLQ routing metric recorded")
}

// TestClusterModeOSSRequiresClusterClient covers part of issue #108: enabling ClusterModeOSS with a
// non-cluster client must fail fast at Init. This runs without any cluster infrastructure.
func TestClusterModeOSSRequiresClusterClient(t *testing.T) {
	_ = os.Setenv("POD_NAME", "oss-validation")

	// a single-address universal client is a *redis.Client, not a *redis.ClusterClient
	singleNode := redisgo.NewUniversalClient(&redisgo.UniversalOptions{Addrs: []string{"localhost:6379"}})
	defer singleNode.Close()

	client, err := impl.NewRedisStreamClient(
		singleNode,
		"consumer",
		impl.WithClusterMode(impl.ClusterModeOSS),
	)
	require.NoError(t, err)

	// Init checks the client type before touching Redis, so this does not require a live server.
	_, err = client.Init(context.Background())
	require.ErrorIs(t, err, errs.ErrClusterClientRequired)
}

// TestOSSClusterKeyspaceSubscription covers issue #115. It only runs when an OSS cluster is provided
// via the REDIS_CLUSTER_ADDRS env var (comma-separated host:port list); otherwise it is skipped.
func TestOSSClusterKeyspaceSubscription(t *testing.T) {
	addrs := os.Getenv("REDIS_CLUSTER_ADDRS")
	if addrs == "" {
		t.Skip("set REDIS_CLUSTER_ADDRS to a comma-separated list of OSS cluster nodes to run this test")
	}

	_ = os.Setenv("POD_NAME", "oss-111")
	cluster := redisgo.NewClusterClient(&redisgo.ClusterOptions{Addrs: splitAndTrim(addrs)})
	defer cluster.Close()

	require.NoError(t, cluster.Ping(context.Background()).Err())

	rec := &testMetricsRecorder{}
	client, err := impl.NewRedisStreamClient(
		cluster,
		"consumer",
		impl.WithForceConfigOverride(),
		impl.WithClusterMode(impl.ClusterModeOSS),
		impl.WithRecoveryConfig(fastRecoveryConfig()),
		impl.WithMetricsRecorder(rec),
	)
	require.NoError(t, err)

	opChan, err := client.Init(context.Background())
	require.NoError(t, err)
	go func() {
		for range opChan {
		}
	}()

	// keyspace notifications must be enabled on every master
	err = cluster.ForEachMaster(context.Background(), func(ctx context.Context, master *redisgo.Client) error {
		vals, gerr := master.ConfigGet(ctx, configs.NotifyKeyspaceEventsCmd).Result()
		require.NoError(t, gerr)
		require.NotEmpty(t, vals)
		return nil
	})
	require.NoError(t, err)

	// topology reset should re-establish subscriptions without error
	require.NoError(t, client.ResetTopology(context.Background()))
	require.GreaterOrEqual(t, rec.TopologyResetCount(), 1)

	require.NoError(t, client.Done(context.Background()))
}

func splitAndTrim(csv string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			seg := csv[start:i]
			// trim surrounding spaces
			for len(seg) > 0 && seg[0] == ' ' {
				seg = seg[1:]
			}
			for len(seg) > 0 && seg[len(seg)-1] == ' ' {
				seg = seg[:len(seg)-1]
			}
			if seg != "" {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	return out
}
