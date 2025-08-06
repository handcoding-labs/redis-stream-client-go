package impl

import (
	"context"

	"github.com/badari31/redis-stream-client-go/types"
)

func (r *RecoverableRedisStreamClient) lbsGroupName() string {
	return r.serviceName + types.GroupSuffix
}

func (r *RecoverableRedisStreamClient) lbsName() string {
	return r.serviceName + types.InputSuffix
}

func (r *RecoverableRedisStreamClient) checkErr(ctx context.Context, fn func(context.Context) error) *RecoverableRedisStreamClient {
	if r == nil {
		return nil
	}

	if err := fn(ctx); err != nil {
		return nil
	}

	return r
}

func (r *RecoverableRedisStreamClient) isContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
