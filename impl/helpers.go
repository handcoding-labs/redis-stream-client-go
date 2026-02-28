package impl

import (
	"context"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
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

func (r *RecoverableRedisStreamClient) cleanup() error {
	// drain kspchan and ignore expired notifications
	// since client has called Done and thus are no longer interested in expired notifications
	for len(r.kspChan) > 0 {
		<-r.kspChan
	}

	// close the output channel
	r.notificationBroker.Close()

	// cancel LBS context
	r.lbsCtxCancelFunc()

	// close pub sub
	if err := r.pubSub.Close(); err != nil {
		r.logger.Error("error closing redis pub sub")
		return errs.NewRedisError(errs.OpClosePubSub, err)
	}

	return nil
}

// popStreamLocksInfo removes the datastream from streamLocks map (internal state) and returns the value
func (r *RecoverableRedisStreamClient) popStreamLocksInfo(dataStreamName string) (*StreamLocksInfo, error) {
	r.streamLocksMutex.Lock()
	streamLocksInfo, ok := r.streamLocks[dataStreamName]
	if !ok {
		r.streamLocksMutex.Unlock()
		return nil, errs.ErrDataStreamNotFound
	}

	// delete volatile key from streamLocks
	delete(r.streamLocks, dataStreamName)
	r.streamLocksMutex.Unlock()

	return streamLocksInfo, nil
}

func (r *RecoverableRedisStreamClient) isStreamProcessingDone(dataStreamName string) bool {
	r.streamLocksMutex.Lock()
	defer r.streamLocksMutex.Unlock()
	return r.streamLocks[dataStreamName] == nil
}
