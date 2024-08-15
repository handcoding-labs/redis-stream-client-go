package impl

import (
	"bburli/redis-stream-client-go/types"
	"context"
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

func (r *ReliableRedisStreamClient) Giveup(ctx context.Context) error {
	for _, s := range r.streamLocks {
		err := r.Done(ctx, s.DataStreamName)
		if err != nil {
			return err
		}

		// cancel lbs context explicitly
		r.lbsCtxCancelFunc()
	}

	return nil
}
