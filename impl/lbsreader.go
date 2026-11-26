package impl

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
)

// The LBS stream is read by two goroutines. The reader only runs the blocking XREADGROUP and hands
// each result to the processor; the processor acquires locks and is the only one that sends
// notifications. Splitting them lets Done interrupt the read instead of waiting for it.
//
// go-redis does not interrupt a blocking read when its context is canceled, and Go cannot kill a
// goroutine, so the reader is interrupted through the resource it is blocked on: it reads on a
// dedicated connection, and when the context is canceled CLIENT UNBLOCK is sent for that connection
// from the shared pool. The read returns at once as if its block had timed out. A reader that were
// simply abandoned would stay parked in XREADGROUP and be handed the next LBS message, which nothing
// would process until the reconciliation scan recovered it.

// lbsRead is one result handed from the reader to the processor.
type lbsRead struct {
	streams []redis.XStream
	// err is set when the reader has given up after maxRetries consecutive errors.
	err error
}

// xReadGrouper is the one command the reader needs, satisfied by both a dedicated *redis.Conn and
// the shared client.
type xReadGrouper interface {
	XReadGroup(ctx context.Context, a *redis.XReadGroupArgs) *redis.XStreamSliceCmd
}

// lbsConn is what the reader issues XREADGROUP on.
type lbsConn struct {
	cmd xReadGrouper
	// conn is the dedicated connection, nil when reading on the shared client.
	conn *redis.Conn
	// block is how long each XREADGROUP may block.
	block time.Duration
	// stop deregisters the CLIENT UNBLOCK hook, nil when there is none.
	stop func() bool
}

func (c *lbsConn) close() {
	if c.stop != nil {
		c.stop()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// lbsNodeClient returns a client for the node that serves the LBS stream, or nil if the client
// type has no way to pin one (for example *redis.Ring).
func (r *RecoverableRedisStreamClient) lbsNodeClient(ctx context.Context) (*redis.Client, error) {
	switch c := r.redisClient.(type) {
	case *redis.Client:
		return c, nil
	case *redis.ClusterClient:
		return c.MasterForKey(ctx, r.lbsName())
	default:
		return nil, nil
	}
}

// openLBSConn returns an interruptible dedicated connection when the deployment allows it, and
// otherwise the shared client with a short bounded block, so that a canceled reader still notices
// within configs.DefaultLBSReadBlock.
func (r *RecoverableRedisStreamClient) openLBSConn(ctx context.Context, readerDone <-chan struct{}) *lbsConn {
	fallback := &lbsConn{cmd: r.redisClient, block: configs.DefaultLBSReadBlock}

	node, err := r.lbsNodeClient(ctx)
	if err != nil {
		r.logger.Warn("cannot pin a connection for the LBS read; using bounded reads", "error", err)
		return fallback
	}
	if node == nil {
		return fallback
	}

	conn := node.Conn()
	id, err := conn.ClientID(ctx).Result()
	if err != nil {
		_ = conn.Close()
		r.logger.Warn("cannot get the LBS connection's client id; using bounded reads", "error", err)
		return fallback
	}

	// CLIENT UNBLOCK for a client that is not blocked returns 0. It fails if the user may not run it
	// (it is in the @admin ACL category, which some managed services deny).
	if err := node.ClientUnblock(ctx, id).Err(); err != nil {
		_ = conn.Close()
		r.logger.Warn("CLIENT UNBLOCK is not permitted; using bounded reads, so Done can take up to "+
			configs.DefaultLBSReadBlock.String(), "error", err)
		return fallback
	}

	stop := context.AfterFunc(ctx, func() {
		// Repeat until the reader has exited: an unblock that reaches the server just before the
		// reader's XREADGROUP does finds nothing to unblock.
		ticker := time.NewTicker(configs.LBSUnblockRetryInterval)
		defer ticker.Stop()
		for {
			_ = node.ClientUnblock(context.Background(), id).Err()
			select {
			case <-readerDone:
				return
			case <-ticker.C:
			}
		}
	})

	return &lbsConn{cmd: conn, conn: conn, block: configs.DefaultLBSInterruptibleReadBlock, stop: stop}
}

// readLBS runs the blocking read on the LBS stream and hands every result to the processor,
// waiting for the processor to finish with it before reading again, so a consumer only claims new
// work once it has taken the locks for the previous batch. It never sends notifications, and it
// closes reads when it exits.
func (r *RecoverableRedisStreamClient) readLBS(ctx context.Context, reads chan<- lbsRead, processed <-chan struct{}) {
	readerDone := make(chan struct{})
	defer close(readerDone)
	defer close(reads)

	var conn *lbsConn
	defer func() {
		if conn != nil {
			conn.close()
		}
	}()

	consecutiveErrors := 0
	currentRetryDelay := r.initialRetryDelay

	for {
		if r.isContextDone(ctx) {
			return
		}

		if conn == nil {
			conn = r.openLBSConn(ctx, readerDone)
		}

		res := conn.cmd.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.lbsGroupName(),
			Consumer: r.consumerID,
			Streams:  []string{r.lbsName(), configs.PendingMsgID},
			Block:    conn.block,
		})

		// redis.Nil means the block timed out or was interrupted by CLIENT UNBLOCK: re-check ctx.
		if errors.Is(res.Err(), redis.Nil) {
			continue
		}

		if err := res.Err(); err != nil {
			if r.isContextDone(ctx) {
				return
			}

			// Drop the connection: after a failover or a slot move the LBS stream may now live on
			// another node, so the next attempt pins a fresh one. A dedicated connection bypasses
			// the cluster client's own MOVED handling, so ask it to refresh the topology now; the
			// reload runs in the background and finishes well within the retry delay.
			conn.close()
			conn = nil
			if cluster, ok := r.redisClient.(*redis.ClusterClient); ok {
				cluster.ReloadState(ctx)
			}

			consecutiveErrors++
			r.logger.Error("error while reading from LBS",
				"error", err,
				"consecutive_errors", consecutiveErrors,
				"retry_delay", currentRetryDelay)

			if r.maxRetries >= 0 && consecutiveErrors > r.maxRetries {
				r.logger.Error("max retries exceeded, terminating stream",
					"max_retries", r.maxRetries,
					"consecutive_errors", consecutiveErrors)
				select {
				case reads <- lbsRead{err: err}:
				case <-ctx.Done():
				}
				return
			}

			// sleep with exponential backoff before retrying
			select {
			case <-ctx.Done():
				return
			case <-time.After(currentRetryDelay):
				currentRetryDelay = currentRetryDelay * 2
				if currentRetryDelay > r.maxRetryDelay {
					currentRetryDelay = r.maxRetryDelay
				}
			}

			continue
		}

		// successful read - reset error tracking
		if consecutiveErrors > 0 {
			r.logger.Info("LBS stream read recovered after errors",
				"consecutive_errors", consecutiveErrors)
			consecutiveErrors = 0
			currentRetryDelay = r.initialRetryDelay
		}

		// If the client stops here, the messages just read stay in this consumer's pending list and
		// the reconciliation scan recovers them, as for a consumer that died.
		select {
		case reads <- lbsRead{streams: res.Val()}:
		case <-ctx.Done():
			return
		}
		select {
		case <-processed:
		case <-ctx.Done():
			return
		}
	}
}

// processLBSStream takes each batch from the reader, acquires locks and notifies the caller. It is
// the only LBS goroutine that sends notifications.
func (r *RecoverableRedisStreamClient) processLBSStream(ctx context.Context, reads <-chan lbsRead, processed chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif("context done"))
			return
		case read, ok := <-reads:
			if !ok {
				// the reader only exits without an error when the context is done
				r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif("context done"))
				return
			}
			if read.err != nil {
				r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif(read.err.Error()))
				return
			}
			if err := r.processLBSMessages(ctx, read.streams, r.rs); err != nil {
				r.logger.Error("fatal error while processing lbs messages", "error", err)
				r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif(err.Error()))
				return
			}
			select {
			case processed <- struct{}{}:
			case <-ctx.Done():
				r.notificationBroker.Send(ctx, notifs.MakeStreamTerminatedNotif("context done"))
				return
			}
		}
	}
}
