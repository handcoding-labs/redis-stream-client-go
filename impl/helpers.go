package impl

import (
	"context"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
)

func (r *RecoverableRedisStreamClient) lbsGroupName() string {
	return r.serviceName + configs.GroupSuffix
}

func (r *RecoverableRedisStreamClient) lbsName() string {
	return r.serviceName + configs.InputSuffix
}

func (r *RecoverableRedisStreamClient) isContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (r *RecoverableRedisStreamClient) isStreamProcessingDone(dataStreamName string) bool {
	r.streamLocksMutex.Lock()
	defer r.streamLocksMutex.Unlock()
	return r.streamLocks[dataStreamName] == nil
}

func (r *RecoverableRedisStreamClient) closeOutputChan() {
	if r.outputChanClosed.CompareAndSwap(false, true) {
		close(r.outputChan)
	}
}
