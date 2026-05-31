package impl

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// ossSubscriptions groups the per-master keyspace-notification subscriptions used in ClusterModeOSS.
// In an OSS Redis Cluster keyspace notifications fire only on the master that owns the expiring key,
// so the client subscribes on every master; this state tracks those subscriptions so they can be
// torn down and rebuilt on topology changes (failover / resharding).
type ossSubscriptions struct {
	mu      sync.Mutex
	pubSubs []*redis.PubSub
}

// add records a newly opened per-master subscription.
func (o *ossSubscriptions) add(ps *redis.PubSub) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pubSubs = append(o.pubSubs, ps)
}

// closeAll closes every tracked subscription and resets the set.
func (o *ossSubscriptions) closeAll(logger *slog.Logger) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, ps := range o.pubSubs {
		if err := ps.Close(); err != nil {
			logger.Warn("error closing OSS keyspace subscription", "error", err)
		}
	}
	o.pubSubs = nil
}

// enableKeyspaceNotifsOnMasters applies the expired-events keyspace config to every master in the
// cluster. It tolerates partial failure: a single unreachable master is logged and recorded via a
// metric rather than aborting the whole sweep, since the periodic reconciliation scan is the
// authoritative recovery path. A deterministic misconfiguration (existing config without force
// override) is fatal and aborts immediately, as is the case where every master fails.
func (r *RecoverableRedisStreamClient) enableKeyspaceNotifsOnMasters(
	ctx context.Context,
	cluster *redis.ClusterClient,
) error {
	var (
		mu        sync.Mutex
		succeeded int
		failed    int
	)

	// ForEachMaster runs the callback concurrently, so guard the counters with the mutex.
	err := cluster.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
		if setupErr := r.enableKeyspaceNotifsOn(ctx, master); setupErr != nil {
			// A missing force-override is a deterministic operator decision that applies to every
			// master equally; propagate it so Init fails fast rather than silently degrading.
			if errors.Is(setupErr, errs.ErrExistingConfigWithoutOverride) {
				return setupErr
			}
			mu.Lock()
			failed++
			mu.Unlock()
			r.metricsRecorder.RecordMasterKeyspaceSetup(false)
			r.logger.Warn("failed to enable keyspace notifications on a master; continuing with the rest",
				"error", setupErr)
			return nil
		}
		mu.Lock()
		succeeded++
		mu.Unlock()
		r.metricsRecorder.RecordMasterKeyspaceSetup(true)
		return nil
	})
	if err != nil {
		return err
	}

	if succeeded == 0 {
		return errs.ErrKeyspaceNotifsAllMastersFailed
	}
	if failed > 0 {
		r.logger.Warn("enabled keyspace notifications on a subset of masters",
			"succeeded", succeeded, "failed", failed)
	}
	return nil
}

// subscribeToExpiredEventsOSS opens a keyspace subscription on every master node and tracks the
// subscriptions so they can be torn down and rebuilt by ResetTopology.
func (r *RecoverableRedisStreamClient) subscribeToExpiredEventsOSS(ctx context.Context) {
	cluster, ok := r.redisClient.(*redis.ClusterClient)
	if !ok {
		r.logger.Error("ClusterModeOSS requires a *redis.ClusterClient; skipping keyspace subscription")
		return
	}

	// ForEachMaster runs the callback concurrently; ossSubscriptions guards its own state.
	err := cluster.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
		ps := master.PSubscribe(ctx, configs.MutexKeySpacePattern)
		r.oss.add(ps)
		r.fanInPubSub(ps)
		return nil
	})
	if err != nil {
		r.logger.Error("error subscribing to keyspace notifications on masters", "error", err)
	}
}
