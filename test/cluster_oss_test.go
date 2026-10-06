package test

// Tests in this file need a real OSS Redis Cluster (REDIS_CLUSTER_ADDRS, see
// test/scripts/redis-cluster.sh) and are skipped otherwise. They cover behaviour that a standalone
// server cannot exhibit: lock keys and keyspace events spread across masters, per-master
// subscriptions, and topology changes.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	redisgo "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/impl"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// clusterShard is a master together with its replicas, as reported by CLUSTER SLOTS.
type clusterShard struct {
	start, end int
	master     string
	replicas   []string
}

func loadClusterShards(cluster *redisgo.ClusterClient) ([]clusterShard, error) {
	slots, err := cluster.ClusterSlots(context.Background()).Result()
	if err != nil {
		return nil, err
	}

	shards := make([]clusterShard, 0, len(slots))
	for _, s := range slots {
		shard := clusterShard{start: s.Start, end: s.End, master: s.Nodes[0].Addr}
		for _, n := range s.Nodes[1:] {
			shard.replicas = append(shard.replicas, n.Addr)
		}
		shards = append(shards, shard)
	}
	return shards, nil
}

func clusterShards(t *testing.T, cluster *redisgo.ClusterClient) []clusterShard {
	t.Helper()
	shards, err := loadClusterShards(cluster)
	require.NoError(t, err)
	return shards
}

// failoverShard gracefully promotes the first replica of shard to master (CLUSTER FAILOVER), waits
// for the cluster to converge, and registers a cleanup that fails back so later tests see the
// original topology. The caller must close cluster with t.Cleanup (not defer) so it is still open
// when that cleanup runs. It returns the demoted and the promoted node.
func failoverShard(t *testing.T, cluster *redisgo.ClusterClient, shard clusterShard) (oldNode, newNode *redisgo.Client) {
	t.Helper()
	require.NotEmpty(t, shard.replicas, "the shard needs a replica to fail over to")
	oldNode, newNode = nodeClient(t, shard.master), nodeClient(t, shard.replicas[0])

	failover := func(to, from *redisgo.Client) {
		require.NoError(t, to.ClusterFailover(context.Background()).Err())
		require.Eventuallyf(t, func() bool {
			cluster.ReloadState(context.Background())
			shards, err := loadClusterShards(cluster)
			if err != nil {
				return false
			}
			for _, s := range shards {
				if s.start == shard.start {
					return s.master == to.Options().Addr && isMaster(to) && !isMaster(from)
				}
			}
			return false
		}, 30*time.Second, 200*time.Millisecond, "failover %s -> %s did not complete",
			from.Options().Addr, to.Options().Addr)

		require.Eventually(t, func() bool {
			info, err := to.ClusterInfo(context.Background()).Result()
			return err == nil && strings.Contains(info, "cluster_state:ok")
		}, 30*time.Second, 200*time.Millisecond, "cluster did not return to state ok")
	}

	failover(newNode, oldNode)
	t.Cleanup(func() { failover(oldNode, newNode) })
	return oldNode, newNode
}

// masterOf returns the address of the master that currently owns key.
func masterOf(cluster *redisgo.ClusterClient, key string) (string, error) {
	node, err := cluster.MasterForKey(context.Background(), key)
	if err != nil {
		return "", err
	}
	return node.Options().Addr, nil
}

// nodeClient returns a client bound to a single node (bypassing cluster routing). Call it from the
// test goroutine only; the returned client is safe to use from polling closures.
func nodeClient(t *testing.T, addr string) *redisgo.Client {
	t.Helper()
	c := redisgo.NewClient(&redisgo.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// pubSubPatternClients counts the connections on a node that currently hold a pattern
// subscription. Unlike PUBSUB NUMPAT (unique patterns) this reveals duplicated/leaked subscriptions.
func pubSubPatternClients(node *redisgo.Client) (int, error) {
	list, err := node.ClientList(context.Background()).Result()
	if err != nil {
		return 0, err
	}

	n := 0
	for _, line := range strings.Split(list, "\n") {
		for _, field := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(field, "psub="); ok && v != "0" {
				n++
			}
		}
	}
	return n, nil
}

// isMaster reports whether the node currently has the master role.
func isMaster(node *redisgo.Client) bool {
	role, err := node.Do(context.Background(), "ROLE").Slice()
	return err == nil && len(role) > 0 && role[0] == "master"
}

// notifLog records the data streams seen on notification channels, per notification type.
type notifLog struct {
	mu   sync.Mutex
	seen map[notifs.NotificationType]map[string]int
}

func newNotifLog() *notifLog {
	return &notifLog{seen: make(map[notifs.NotificationType]map[string]int)}
}

func (l *notifLog) drain(ch <-chan notifs.RecoverableRedisNotification) {
	go func() {
		for msg := range ch {
			l.mu.Lock()
			if l.seen[msg.Type] == nil {
				l.seen[msg.Type] = make(map[string]int)
			}
			l.seen[msg.Type][msg.Payload.DataStreamName]++
			l.mu.Unlock()
		}
	}()
}

func (l *notifLog) count(typ notifs.NotificationType) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen[typ])
}

// TestOSSClusterRecoversStreamsLockedOnEveryMaster covers #108/#114 end to end on a real cluster.
//
// A consumer holds many streams, so their lock keys hash to every master. When it dies, each
// master must publish the lock-expiry events, every surviving consumer must hear about all of them
// (it subscribes on every master), and the reconciliation scan must hand every stream to a survivor.
func TestOSSClusterRecoversStreamsLockedOnEveryMaster(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()

	// Enough streams that all masters own at least one lock key with overwhelming probability
	// (checked below rather than assumed).
	const numStreams = 30

	victimCtx, killVictim := context.WithCancel(ctx)
	victim, _ := createConsumer("000", cl)
	victimChan, err := victim.Init(victimCtx)
	require.NoError(t, err)

	addNStreamsToLBS(t, cl, numStreams)

	held := make(map[string]notifs.LBSInfo)
	for len(held) < numStreams {
		msg := waitForStreamAdded(t, victimChan, "", 15*time.Second)
		held[msg.Payload.DataStreamName] = msg.Payload
	}

	// the lock keys must really be spread across all masters, otherwise this test proves nothing
	cluster := cl.newClient().(*redisgo.ClusterClient)
	defer cluster.Close()
	lockOwners := make(map[string]bool)
	for _, info := range held {
		owner, oerr := masterOf(cluster, info.FormMutexKey())
		require.NoError(t, oerr)
		lockOwners[owner] = true
	}
	require.Len(t, lockOwners, len(clusterShards(t, cluster)), "lock keys should span every master")

	// two survivors, each wired to observe expiry notifications and the StreamAdded after recovery
	obs1, rec1 := createConsumerWithRecovery("111", cl)
	op1, err := obs1.Init(ctx)
	require.NoError(t, err)
	obs2, rec2 := createConsumerWithRecovery("222", cl)
	op2, err := obs2.Init(ctx)
	require.NoError(t, err)

	log1, log2 := newNotifLog(), newNotifLog()
	log1.drain(op1)
	log2.drain(op2)

	// the survivors configured every master and subscribed on each
	for _, rec := range []*testMetricsRecorder{rec1, rec2} {
		require.Equal(t, len(lockOwners), rec.MasterKeyspaceSetupSuccessCount())
		require.Equal(t, 0, rec.MasterKeyspaceSetupFailureCount())
	}

	killVictim()

	// every survivor hears about every expired lock, no matter which master owned it
	require.Eventually(t, func() bool {
		return log1.count(notifs.StreamExpired) == numStreams && log2.count(notifs.StreamExpired) == numStreams
	}, 20*time.Second, 200*time.Millisecond,
		"each survivor should receive an expiry notification for all %d streams (got %d and %d)",
		numStreams, log1.count(notifs.StreamExpired), log2.count(notifs.StreamExpired))

	// and the periodic scan re-queues every stream to a survivor
	require.Eventually(t, func() bool {
		merged := make(map[string]struct{})
		for _, l := range []*notifLog{log1, log2} {
			l.mu.Lock()
			for name := range l.seen[notifs.StreamAdded] {
				merged[name] = struct{}{}
			}
			l.mu.Unlock()
		}
		return len(merged) == numStreams
	}, 30*time.Second, 200*time.Millisecond, "survivors should take over all streams")

	require.GreaterOrEqual(t, rec1.ReQueueCount()+rec2.ReQueueCount(), numStreams)

	require.NoError(t, obs1.Done(ctx))
	require.NoError(t, obs2.Done(ctx))
}

// TestOSSClusterSubscriptionsAreNotLeaked covers #108/#109: exactly one pattern subscription per
// master is open while the client runs, ResetTopology replaces (rather than adds to) them, and Done
// closes all of them.
func TestOSSClusterSubscriptionsAreNotLeaked(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()

	cluster := cl.newClient().(*redisgo.ClusterClient)
	defer cluster.Close()

	// Subscriptions left behind by earlier tests whose clients were stopped without Done are still
	// open on the masters, so assert on the change relative to what is already there.
	masters := make(map[string]*redisgo.Client)
	baseline := make(map[string]int)
	for _, shard := range clusterShards(t, cluster) {
		masters[shard.master] = nodeClient(t, shard.master)
		n, err := pubSubPatternClients(masters[shard.master])
		require.NoError(t, err)
		baseline[shard.master] = n
	}

	requireDelta := func(want int, msg string) {
		t.Helper()
		for addr, node := range masters {
			require.Eventuallyf(t, func() bool {
				n, err := pubSubPatternClients(node)
				return err == nil && n-baseline[addr] == want
			}, 5*time.Second, 100*time.Millisecond, "%s: master %s", msg, addr)
		}
	}

	client, rec := createConsumer("111", cl)
	opChan, err := client.Init(ctx)
	require.NoError(t, err)
	requireDelta(1, "one subscription per master after Init")

	for i := 0; i < 3; i++ {
		require.NoError(t, client.ResetTopology(ctx))
	}
	require.Equal(t, 3, rec.TopologyResetCount())
	requireDelta(1, "ResetTopology must replace subscriptions, not stack them")

	require.NoError(t, client.Done(ctx))
	requireDelta(0, "Done must close every per-master subscription")

	_, ok := <-opChan
	require.False(t, ok)
}

// TestOSSClusterInitRefusesExistingConfigWithoutOverride covers the per-master config check: when a
// master already has keyspace-notification config and force override is off, Init must fail rather
// than silently clobber it.
func TestOSSClusterInitRefusesExistingConfigWithoutOverride(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()
	_ = os.Setenv("POD_NAME", "no-override")

	cluster := cl.newClient().(*redisgo.ClusterClient)
	defer cluster.Close()
	require.NoError(t, cluster.ForEachMaster(ctx, func(ctx context.Context, m *redisgo.Client) error {
		return m.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, "Kg").Err()
	}))
	// Redis normalises the flag order, so remember what each master reports rather than what we set
	var mu sync.Mutex
	before := make(map[string]string)
	require.NoError(t, cluster.ForEachMaster(ctx, func(ctx context.Context, m *redisgo.Client) error {
		vals, gerr := m.ConfigGet(ctx, configs.NotifyKeyspaceEventsCmd).Result()
		mu.Lock()
		defer mu.Unlock()
		before[m.Options().Addr] = vals[configs.NotifyKeyspaceEventsCmd]
		return gerr
	}))

	client, err := impl.NewRedisStreamClient(cl.newClient(), "consumer", impl.WithClusterMode(impl.ClusterModeOSS))
	require.NoError(t, err)

	_, err = client.Init(ctx)
	require.ErrorIs(t, err, errs.ErrExistingConfigWithoutOverride)

	// the pre-existing config was left alone
	for addr, want := range before {
		vals, gerr := nodeClient(t, addr).ConfigGet(ctx, configs.NotifyKeyspaceEventsCmd).Result()
		require.NoError(t, gerr)
		require.Equal(t, want, vals[configs.NotifyKeyspaceEventsCmd], "master %s config was modified", addr)
	}
}

// TestOSSClusterResetTopologyWithoutForceOverride: ResetTopology is documented as safe to call at any
// time (e.g. after a failover), so it must work for a client that did not opt in to
// WithForceConfigOverride. The keyspace config it meets on the masters is the config this very
// client applied during Init.
func TestOSSClusterResetTopologyWithoutForceOverride(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()
	_ = os.Setenv("POD_NAME", "no-override-reset")

	rec := &testMetricsRecorder{}
	client, err := impl.NewRedisStreamClient(cl.newClient(), "consumer",
		impl.WithClusterMode(impl.ClusterModeOSS), impl.WithMetricsRecorder(rec))
	require.NoError(t, err)

	opChan, err := client.Init(ctx)
	require.NoError(t, err, "Init on a pristine cluster must succeed without force override")
	go func() {
		for range opChan {
		}
	}()

	require.NoError(t, client.ResetTopology(ctx),
		"ResetTopology must not trip over the config this client itself applied")
	require.Equal(t, 1, rec.TopologyResetCount())

	require.NoError(t, client.Done(ctx))
}

// TestOSSClusterFailoverThenResetTopology covers #109 against a real failover: a replica is promoted
// to master. Keyspace config is per node, so the promoted node does not emit expiry events until
// ResetTopology re-applies it and re-subscribes; afterwards a lock expiring on the new master must
// reach the client.
func TestOSSClusterFailoverThenResetTopology(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()

	// closed via t.Cleanup (not defer) so it is still open while the failback cleanup below runs
	cluster := cl.newClient().(*redisgo.ClusterClient)
	t.Cleanup(func() { _ = cluster.Close() })

	var shard clusterShard
	for _, s := range clusterShards(t, cluster) {
		if len(s.replicas) > 0 {
			shard = s
			break
		}
	}
	require.NotEmpty(t, shard.replicas, "the cluster needs at least one replica to fail over to")

	client, rec := createConsumer("111", cl)
	opChan, err := client.Init(ctx)
	require.NoError(t, err)
	log := newNotifLog()
	log.drain(opChan)

	// a lock-style key (data stream "failover-stream", id "1") owned by the shard we fail over
	var key string
	for i := 0; ; i++ {
		key = fmt.Sprintf("failover-stream-%d%s1", i, configs.MutexKeySep)
		slot, kerr := cluster.ClusterKeySlot(ctx, key).Result()
		require.NoError(t, kerr)
		if int(slot) >= shard.start && int(slot) <= shard.end {
			break
		}
		require.Less(t, i, 100000, "could not find a key in the shard's slot range")
	}
	dataStream := strings.Split(key, configs.MutexKeySep)[0]

	// graceful failover: promote the replica and demote the old master
	_, newNode := failoverShard(t, cluster, shard)

	// the promoted node never had keyspace notifications enabled (config is not replicated)
	vals, err := newNode.ConfigGet(ctx, configs.NotifyKeyspaceEventsCmd).Result()
	require.NoError(t, err)
	require.Empty(t, vals[configs.NotifyKeyspaceEventsCmd], "promoted replica should start without keyspace config")

	require.NoError(t, client.ResetTopology(ctx))
	require.Equal(t, 1, rec.TopologyResetCount())

	vals, err = newNode.ConfigGet(ctx, configs.NotifyKeyspaceEventsCmd).Result()
	require.NoError(t, err)
	require.NotEmpty(t, vals[configs.NotifyKeyspaceEventsCmd], "ResetTopology must enable keyspace events on the new master")
	subs, err := pubSubPatternClients(newNode)
	require.NoError(t, err)
	require.Equal(t, 1, subs, "one subscription on the new master")

	// a lock expiring on the new master is now observed
	require.NoError(t, cluster.Set(ctx, key, "owner", time.Second).Err())
	require.Eventually(t, func() bool { return log.count(notifs.StreamExpired) >= 1 },
		10*time.Second, 100*time.Millisecond, "expiry of %q on the new master was not delivered", key)
	log.mu.Lock()
	require.Contains(t, log.seen[notifs.StreamExpired], dataStream)
	log.mu.Unlock()

	require.NoError(t, client.Done(ctx))
}

// TestOSSClusterKeyspaceSetupMetrics checks the per-master setup metric: with every master healthy
// each one is recorded as a success on Init and again on ResetTopology, and none as a failure.
func TestOSSClusterKeyspaceSetupMetrics(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()

	cluster := cl.newClient().(*redisgo.ClusterClient)
	defer cluster.Close()
	masters := len(clusterShards(t, cluster))

	client, rec := createConsumer("111", cl)
	_, err := client.Init(ctx)
	require.NoError(t, err)
	require.Equal(t, masters, rec.MasterKeyspaceSetupSuccessCount())
	require.Equal(t, 0, rec.MasterKeyspaceSetupFailureCount())

	require.NoError(t, client.ResetTopology(ctx))
	require.Equal(t, 2*masters, rec.MasterKeyspaceSetupSuccessCount(), "ResetTopology re-applies config on every master")
	require.NoError(t, client.Done(ctx))
}

// TestOSSClusterLocksSurviveGracefulFailover: a consumer's lock keys live on the masters. When a
// master is gracefully failed over to its replica, the consumer must keep its streams: lock
// extension keeps working (no StreamDisowned), the lock keys still exist, and another consumer's
// aggressive reconciliation scan finds every owner alive instead of re-queuing live work.
func TestOSSClusterLocksSurviveGracefulFailover(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()
	const numStreams = 12

	cluster := cl.newClient().(*redisgo.ClusterClient)
	t.Cleanup(func() { _ = cluster.Close() })

	holder, holderRec := createConsumerWithRecovery("111", cl)
	holderChan, err := holder.Init(ctx)
	require.NoError(t, err)

	addNStreamsToLBS(t, cl, numStreams)
	lockKeys := make(map[string]string) // data stream -> lock key
	for len(lockKeys) < numStreams {
		msg := waitForStreamAdded(t, holderChan, "", 15*time.Second)
		lockKeys[msg.Payload.DataStreamName] = msg.Payload.FormMutexKey()
	}
	holderLog := newNotifLog()
	holderLog.drain(holderChan)

	scanner, scannerRec := createConsumerWithRecovery("222", cl)
	scannerChan, err := scanner.Init(ctx)
	require.NoError(t, err)
	scannerLog := newNotifLog()
	scannerLog.drain(scannerChan)

	// fail over the shard that owns the most lock keys
	perMaster := make(map[string]int)
	for _, key := range lockKeys {
		owner, oerr := masterOf(cluster, key)
		require.NoError(t, oerr)
		perMaster[owner]++
	}
	var target clusterShard
	for _, s := range clusterShards(t, cluster) {
		if len(s.replicas) > 0 && perMaster[s.master] > perMaster[target.master] {
			target = s
		}
	}
	require.NotEmpty(t, target.master, "no shard with replicas owns a lock key")
	failoverShard(t, cluster, target)

	// let several heartbeat periods and reconciliation scans go by on the new topology
	time.Sleep(8 * time.Second)

	require.Equal(t, 0, holderLog.count(notifs.StreamDisowned), "the holder must not lose any stream to the failover")
	require.Equal(t, 0, holderRec.ReQueueCount()+scannerRec.ReQueueCount(), "live streams must not be re-queued")
	require.Equal(t, 0, scannerLog.count(notifs.StreamAdded), "the scanner must not receive any stream")
	require.Equal(t, 0, holderRec.StreamProcessingEndCount())

	for stream, key := range lockKeys {
		exists, eerr := cluster.Exists(ctx, key).Result()
		require.NoError(t, eerr)
		require.Equal(t, int64(1), exists, "lock for %s should still be held", stream)
	}

	require.NoError(t, holder.Done(ctx))
	require.NoError(t, scanner.Done(ctx))
}
