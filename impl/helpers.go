package impl

import (
	"bburli/redis-stream-client-go/types"
	"context"
	"strings"

	"github.com/go-redsync/redsync/v4"
)

func (r *ReliableRedisStreamClient) lbsGroupName() string {
	return r.serviceName + types.GroupSuffix
}

func (r *ReliableRedisStreamClient) lbsName() string {
	return r.serviceName + types.InputSuffix
}

func (r *ReliableRedisStreamClient) checkErr(ctx context.Context, fn func(context.Context) error) *ReliableRedisStreamClient {
	if r == nil {
		return nil
	}

	if err := fn(ctx); err != nil {
		return nil
	}

	return r
}

func (r *ReliableRedisStreamClient) StreamsOwned() (streamsOwned []string) {
	for _, s := range r.streamLocks {
		streamsOwned = append(streamsOwned, s.DataStreamName)
	}

	return
}

func (r *ReliableRedisStreamClient) isContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (r *ReliableRedisStreamClient) lockAndExtend(mutex *redsync.Mutex) error {
	lockAlreadyTaken := func(err error) bool { return strings.Contains(err.Error(), types.LockAlreadyTakenErrMsg) }

	if err := mutex.Lock(); err != nil && !lockAlreadyTaken(err) {
		return err
	}

	_, err := mutex.Extend()
	if err != nil && !lockAlreadyTaken(err) {
		return err
	}

	return nil
}
