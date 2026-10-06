//go:build nocontainers

package test

import "testing"

// startRedisContainer is the Docker-free build (-tags nocontainers): there is no container to start,
// so a backend must be supplied through REDIS_ADDR or REDIS_CLUSTER_ADDRS.
func startRedisContainer(t *testing.T) *testRedis {
	t.Fatalf("built with -tags nocontainers: set %s or %s", envRedisAddr, envClusterAddrs)
	return nil
}
