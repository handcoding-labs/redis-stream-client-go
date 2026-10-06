package test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	redisgo "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/impl"
)

// Environment variables that select the Redis backend the suite runs against. With neither set the
// suite starts a standalone Redis container per test (see env_container_test.go).
const (
	// envClusterAddrs is a comma-separated list of OSS Redis Cluster node addresses. When set, the
	// suite runs against that cluster using a *redis.ClusterClient and ClusterModeOSS.
	// test/scripts/redis-cluster.sh starts a local one without Docker.
	envClusterAddrs = "REDIS_CLUSTER_ADDRS"
	// envRedisAddr is a single standalone Redis address (host:port), used instead of a container.
	envRedisAddr = "REDIS_ADDR"
)

// testRedis describes the Redis deployment a test runs against: a standalone server (container or
// external) or an OSS Redis Cluster.
type testRedis struct {
	// clusterAddrs is non-empty for an OSS Redis Cluster.
	clusterAddrs []string
	// addr is the standalone server address (host:port) when clusterAddrs is empty.
	addr string

	// cancels stops every library client started through createConsumer. A shared server outlives
	// the test, so a client the test forgot to stop would otherwise keep reading the LBS stream and
	// steal work from later tests.
	mu      sync.Mutex
	cancels []context.CancelFunc
}

// trackCancel registers a cancel func to be called when the test finishes.
func (r *testRedis) trackCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, cancel)
}

// stopClients cancels every tracked client context.
func (r *testRedis) stopClients() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.cancels {
		cancel()
	}
	r.cancels = nil
}

func (r *testRedis) isCluster() bool {
	return len(r.clusterAddrs) > 0
}

// newClient returns a fresh client for the deployment: a *redis.ClusterClient for a cluster,
// otherwise a standalone client.
func (r *testRedis) newClient() redisgo.UniversalClient {
	if r.isCluster() {
		return redisgo.NewClusterClient(&redisgo.ClusterOptions{Addrs: r.clusterAddrs})
	}
	return redisgo.NewUniversalClient(&redisgo.UniversalOptions{Addrs: []string{r.addr}, DB: 0})
}

// clientOptions are the options every library client needs for this deployment (ClusterModeOSS for
// a cluster, nothing for standalone).
func (r *testRedis) clientOptions() []impl.RecoverableRedisOption {
	if r.isCluster() {
		return []impl.RecoverableRedisOption{impl.WithClusterMode(impl.ClusterModeOSS)}
	}
	return nil
}

// reset puts a shared (non-container) deployment back to the state of a fresh server: no keys and
// no keyspace-notification config. In a cluster the config is cleared on replicas as well, because
// a replica promoted by a failover starts using its own (possibly stale) config.
func (r *testRedis) reset(t testing.TB) {
	t.Helper()
	ctx := context.Background()
	client := r.newClient()
	defer client.Close()

	clearConfig := func(ctx context.Context, c *redisgo.Client) error {
		return c.ConfigSet(ctx, configs.NotifyKeyspaceEventsCmd, "").Err()
	}
	wipe := func(ctx context.Context, c *redisgo.Client) error {
		if err := c.FlushAll(ctx).Err(); err != nil {
			return err
		}
		return clearConfig(ctx, c)
	}

	if cluster, ok := client.(*redisgo.ClusterClient); ok {
		require.NoError(t, cluster.ForEachMaster(ctx, wipe))
		require.NoError(t, cluster.ForEachSlave(ctx, clearConfig))
		return
	}
	require.NoError(t, wipe(ctx, client.(*redisgo.Client)))
}

// setupSuite returns the Redis deployment for a test. It uses REDIS_CLUSTER_ADDRS or REDIS_ADDR when
// set (wiping the deployment before and after the test so tests stay independent), and otherwise
// starts a Redis container.
func setupSuite(t *testing.T) *testRedis {
	t.Helper()

	var r *testRedis
	switch {
	case os.Getenv(envClusterAddrs) != "":
		r = &testRedis{clusterAddrs: splitAndTrim(os.Getenv(envClusterAddrs))}
	case os.Getenv(envRedisAddr) != "":
		r = &testRedis{addr: strings.TrimSpace(os.Getenv(envRedisAddr))}
	default:
		r = startRedisContainer(t)
		t.Cleanup(r.stopClients)
		return r
	}

	r.reset(t)
	// Cleanups run last-in-first-out: stop the clients first, then wipe the server. Wiping also
	// unblocks any XREADGROUP the stopped clients are still parked in.
	t.Cleanup(func() { r.reset(t) })
	t.Cleanup(r.stopClients)
	return r
}

// requireCluster returns the OSS cluster deployment, skipping the test when none is configured.
func requireCluster(t *testing.T) *testRedis {
	t.Helper()
	if os.Getenv(envClusterAddrs) == "" {
		t.Skipf("set %s (see test/scripts/redis-cluster.sh) to run this test", envClusterAddrs)
	}
	return setupSuite(t)
}

func splitAndTrim(csv string) []string {
	var out []string
	for _, seg := range strings.Split(csv, ",") {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}
