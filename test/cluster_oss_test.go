package test

// Tests in this file need a real OSS Redis Cluster (REDIS_CLUSTER_ADDRS, see
// test/scripts/redis-cluster.sh) and are skipped otherwise. They cover behavior that a standalone
// server cannot exhibit: lock keys and keyspace events spread across masters, per-master
// subscriptions, and topology changes.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
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

	allNodes := clusterNodeClients(t, cluster)

	failover := func(to, from *redisgo.Client) {
		// A graceful failover only works when every node already agrees on the topology: the old
		// master ignores the request from a node it does not (yet) list as its replica, and the
		// election needs the voters to see the same epoch. Right after a role change that is not
		// the case for a moment, and the request then just hangs until Redis gives up after 5s
		// ("Manual failover timed out") while the old master has paused its writes. So request the
		// failover only from a settled cluster, and then expect it to complete.
		waitUntil(t, 90*time.Second, 100*time.Millisecond, func() (bool, string) {
			if left := electionCooldownLeft(to); left > 0 {
				return false, to.Options().Addr + " ran a failover election recently; waiting " + left.Round(time.Second).String()
			}
			if ok, why := clusterSettled(allNodes); !ok {
				return false, why
			}
			if isMaster(to) || !replicaLinkUp(to) {
				return false, to.Options().Addr + " is not an in-sync replica"
			}
			return true, ""
		}, "cluster did not settle before the failover")

		noteElection(to.Options().Addr)
		require.NoError(t, to.ClusterFailover(context.Background()).Err())

		waitUntil(t, 30*time.Second, 100*time.Millisecond, func() (bool, string) {
			cluster.ReloadState(context.Background())
			shards, err := loadClusterShards(cluster)
			if err != nil {
				return false, err.Error()
			}
			for _, s := range shards {
				if s.start == shard.start {
					if s.master == to.Options().Addr && isMaster(to) && !isMaster(from) {
						return true, ""
					}
					return false, "slot range " + strconv.Itoa(s.start) + " is still owned by " + s.master + "\n" +
						describeViews(from, to)
				}
			}
			return false, "shard not found"
		}, "failover "+from.Options().Addr+" -> "+to.Options().Addr+" did not complete")

		// Hand control back only once every node agrees on the new topology. Until then a client
		// asking an arbitrary node for the topology (as ReinitTopology does) can get the old one.
		waitUntil(t, 30*time.Second, 100*time.Millisecond, func() (bool, string) {
			return clusterSettled(allNodes)
		}, "cluster did not settle after the failover")
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

// replicaLinkUp reports whether a replica is connected to its master (initial or re-sync finished).
func replicaLinkUp(node *redisgo.Client) bool {
	info, err := node.Info(context.Background(), "replication").Result()
	return err == nil && strings.Contains(info, "master_link_status:up")
}

// A replica that has just run a failover election keeps stale election state for a while, and a
// manual failover requested from it in that time stalls: the replica announces that it won without
// holding a new election, the cluster does not promote it, and the old master stays paused until
// Redis gives up after 5s ("Manual failover timed out"). Redis exposes no status for this, so the
// tests record when each node last took part in a failover as the target and keep clear of reusing
// it until a cool-down (a multiple of cluster-node-timeout) has passed.
var electionLedger = struct {
	sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

func noteElection(addr string) {
	electionLedger.Lock()
	defer electionLedger.Unlock()
	electionLedger.last[addr] = time.Now()
}

// electionCooldownLeft returns how much longer node must be left alone before it can be the target
// of another failover, 0 if it has not been one recently.
func electionCooldownLeft(node *redisgo.Client) time.Duration {
	electionLedger.Lock()
	at, ok := electionLedger.last[node.Options().Addr]
	electionLedger.Unlock()
	if !ok {
		return 0
	}
	// the election retry window is 4x cluster-node-timeout; add a second of margin
	nodeTimeout := 15 * time.Second // Redis default if the config cannot be read
	if cfg, err := node.ConfigGet(context.Background(), "cluster-node-timeout").Result(); err == nil {
		if ms, perr := strconv.Atoi(cfg["cluster-node-timeout"]); perr == nil {
			nodeTimeout = time.Duration(ms) * time.Millisecond
		}
	}
	if left := 4*nodeTimeout + time.Second - time.Since(at); left > 0 {
		return left
	}
	return 0
}

// pickFailoverShard returns the shard to fail over. Among shards that have a replica it prefers one
// whose replica has not been a failover target recently (so no cool-down wait is needed), then the
// highest score, then the lowest slot range.
func pickFailoverShard(t *testing.T, cluster *redisgo.ClusterClient, score func(clusterShard) int) clusterShard {
	t.Helper()
	var candidates []clusterShard
	waitUntil(t, 30*time.Second, 100*time.Millisecond, func() (bool, string) {
		cluster.ReloadState(context.Background())
		shards, err := loadClusterShards(cluster)
		if err != nil {
			return false, err.Error()
		}
		candidates = candidates[:0]
		for _, s := range shards {
			if len(s.replicas) > 0 {
				candidates = append(candidates, s)
			}
		}
		return len(candidates) > 0, "no shard has a known, in-sync replica to fail over to"
	}, "the cluster needs at least one replica to fail over to")

	fresh := func(s clusterShard) bool { return electionCooldownLeft(nodeClient(t, s.replicas[0])) == 0 }
	sc := func(s clusterShard) int {
		if score == nil {
			return 0
		}
		return score(s)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if fa, fb := fresh(a), fresh(b); fa != fb {
			return fa
		}
		if sa, sb := sc(a), sc(b); sa != sb {
			return sa > sb
		}
		return a.start < b.start
	})
	return candidates[0]
}

// waitUntil polls cond on the test goroutine until it holds, failing the test with cond's last
// explanation if it does not within timeout.
func waitUntil(t testing.TB, timeout, interval time.Duration, cond func() (bool, string), what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, why := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s within %v: %s", what, timeout, why)
		}
		time.Sleep(interval)
	}
}

// clusterNodeClients returns a client for every node (masters and replicas) the cluster knows about.
func clusterNodeClients(t *testing.T, cluster *redisgo.ClusterClient) []*redisgo.Client {
	t.Helper()
	raw, err := cluster.ClusterNodes(context.Background()).Result()
	require.NoError(t, err)

	var nodes []*redisgo.Client
	for _, line := range strings.Split(raw, "\n") {
		if f := strings.Fields(line); len(f) > 1 {
			nodes = append(nodes, nodeClient(t, strings.SplitN(f[1], "@", 2)[0]))
		}
	}
	require.NotEmpty(t, nodes)
	return nodes
}

// nodeView reduces one node's CLUSTER NODES output to what every node must agree on before a
// failover: each node's id, role, master, config epoch, link state and slots. The "myself" marker is
// dropped because it differs per observer. It reports why the view is not usable if any node is
// failing, still in handshake or disconnected.
func nodeView(raw string) (view string, problem string) {
	var rows []string
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		flags := strings.TrimPrefix(strings.ReplaceAll(f[2], "myself,", ""), "myself")
		for _, bad := range []string{"fail", "handshake", "noaddr"} {
			if strings.Contains(flags, bad) {
				return "", f[1] + " is flagged " + f[2]
			}
		}
		if f[7] != "connected" {
			return "", f[1] + " link state is " + f[7]
		}
		// id, role, master id, config epoch, link state, slots
		rows = append(rows, strings.Join(append([]string{f[0], flags, f[3], f[6], f[7]}, f[8:]...), " "))
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n"), ""
}

// clusterSettled reports whether every node is healthy and they all report the same cluster state,
// current epoch and topology, i.e. no role change is still propagating.
func clusterSettled(nodes []*redisgo.Client) (bool, string) {
	ctx := context.Background()
	var wantView, wantEpoch string
	for i, n := range nodes {
		addr := n.Options().Addr
		info, err := n.ClusterInfo(ctx).Result()
		if err != nil {
			return false, addr + ": CLUSTER INFO: " + err.Error()
		}
		if !strings.Contains(info, "cluster_state:ok") {
			return false, addr + ": cluster_state is not ok"
		}
		epoch := ""
		for _, line := range strings.Split(info, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "cluster_current_epoch:"); ok {
				epoch = v
			}
		}
		raw, err := n.ClusterNodes(ctx).Result()
		if err != nil {
			return false, addr + ": CLUSTER NODES: " + err.Error()
		}
		view, problem := nodeView(raw)
		if problem != "" {
			return false, addr + ": " + problem
		}
		if i == 0 {
			wantView, wantEpoch = view, epoch
			continue
		}
		if epoch != wantEpoch {
			return false, addr + " reports epoch " + epoch + ", " + nodes[0].Options().Addr + " reports " + wantEpoch
		}
		if view != wantView {
			return false, addr + " has a different topology than " + nodes[0].Options().Addr
		}
	}
	return true, ""
}

// describeViews renders what two nodes currently think the topology is, for failure messages.
func describeViews(nodes ...*redisgo.Client) string {
	var b strings.Builder
	for _, n := range nodes {
		raw, err := n.ClusterNodes(context.Background()).Result()
		if err != nil {
			raw = err.Error()
		}
		b.WriteString("view of " + n.Options().Addr + ":\n" + raw + "\n")
	}
	return b.String()
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
	cluster := cl.newClusterClient(t)
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
// master is open while the client runs, ReinitTopology replaces (rather than adds to) them, and Done
// closes all of them.
func TestOSSClusterSubscriptionsAreNotLeaked(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()

	cluster := cl.newClusterClient(t)
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
		require.NoError(t, client.ReinitTopology(ctx))
	}
	require.Equal(t, 3, rec.TopologyReinitCount())
	requireDelta(1, "ReinitTopology must replace subscriptions, not stack them")

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

	cluster := cl.newClusterClient(t)
	defer cluster.Close()
	require.NoError(t, cluster.ForEachMaster(ctx, func(ctx context.Context, m *redisgo.Client) error {
		return m.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, "Kg").Err()
	}))
	// Redis normalizes the flag order, so remember what each master reports rather than what we set
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

// TestOSSClusterReinitTopologyWithoutForceOverride: ReinitTopology is documented as safe to call at any
// time (e.g. after a failover), so it must work for a client that did not opt in to
// WithForceConfigOverride. The keyspace config it meets on the masters is the config this very
// client applied during Init.
func TestOSSClusterReinitTopologyWithoutForceOverride(t *testing.T) {
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

	require.NoError(t, client.ReinitTopology(ctx),
		"ReinitTopology must not trip over the config this client itself applied")
	require.Equal(t, 1, rec.TopologyReinitCount())

	require.NoError(t, client.Done(ctx))
}

// TestOSSClusterFailoverThenReinitTopology covers #109 against a real failover: a replica is promoted
// to master. Keyspace config is per node, so the promoted node does not emit expiry events until
// ReinitTopology re-applies it and re-subscribes; afterwards a lock expiring on the new master must
// reach the client.
func TestOSSClusterFailoverThenReinitTopology(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()

	// closed via t.Cleanup (not defer) so it is still open while the failback cleanup below runs
	cluster := cl.newClusterClient(t)
	t.Cleanup(func() { _ = cluster.Close() })

	shard := pickFailoverShard(t, cluster, nil)

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

	require.NoError(t, client.ReinitTopology(ctx))
	require.Equal(t, 1, rec.TopologyReinitCount())

	vals, err = newNode.ConfigGet(ctx, configs.NotifyKeyspaceEventsCmd).Result()
	require.NoError(t, err)
	require.NotEmpty(t, vals[configs.NotifyKeyspaceEventsCmd], "ReinitTopology must enable keyspace events on the new master")
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
// each one is recorded as a success on Init and again on ReinitTopology, and none as a failure.
func TestOSSClusterKeyspaceSetupMetrics(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()

	cluster := cl.newClusterClient(t)
	defer cluster.Close()
	masters := len(clusterShards(t, cluster))

	client, rec := createConsumer("111", cl)
	_, err := client.Init(ctx)
	require.NoError(t, err)
	require.Equal(t, masters, rec.MasterKeyspaceSetupSuccessCount())
	require.Equal(t, 0, rec.MasterKeyspaceSetupFailureCount())

	require.NoError(t, client.ReinitTopology(ctx))
	require.Equal(t, 2*masters, rec.MasterKeyspaceSetupSuccessCount(), "ReinitTopology re-applies config on every master")
	require.NoError(t, client.Done(ctx))
}

// TestOSSClusterLocksSurviveGracefulFailover: a consumer's lock keys live on the masters. When a
// master is gracefully failed over to its replica, the consumer must keep its streams: lock
// extension keeps working (no StreamDisowned), the lock keys still exist, and another consumer's
// aggressive reconciliation scan finds every owner alive instead of re-queuing live work.
func TestOSSClusterLocksSurviveGracefulFailover(t *testing.T) {
	cl := requireCluster(t)
	ctx := context.Background()
	const numStreams = 30 // enough that any given shard owns some lock keys

	cluster := cl.newClusterClient(t)
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
	target := pickFailoverShard(t, cluster, func(s clusterShard) int { return perMaster[s.master] })
	require.Positive(t, perMaster[target.master], "the shard to fail over owns no lock key")
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
