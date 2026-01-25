package impl

import (
	"context"
	"log/slog"
	"os"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/types"
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
	if err := r.pubSub.Close(); err != nil {
		r.logger.Error("error closing redis pub sub")
		return types.ErrClosingRedisPubsub
	}

	// drain kspchan and ignore expired notifications
	// since client has called Done and thus are no longer interested in expired notifications
	for len(r.kspChan) > 0 {
		<-r.kspChan
	}

	// close the output channel
	r.notificationBroker.Close()

	// cancel LBS context
	r.lbsCtxCancelFunc()

	return nil
}

// popStreamLocksInfo removes the datastream from streamLocks map (internal state) and returns the value
func (r *RecoverableRedisStreamClient) popStreamLocksInfo(dataStreamName string) (*StreamLocksInfo, error) {
	r.streamLocksMutex.Lock()
	streamLocksInfo, ok := r.streamLocks[dataStreamName]
	if !ok {
		r.streamLocksMutex.Unlock()
		return nil, types.ErrDataStreamNotFound
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

// getGoogleCloudLogger returns a slog.Logger that writes to stdout.
// This logger is compatible with Google Cloud Logging; see
// https://cloud.google.com/logging/docs/structured-logging for more
// details on structured logging that Cloud Logging expects.
func getGoogleCloudLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.LevelKey:
				a.Key = "severity"
				if level, ok := a.Value.Any().(slog.Level); ok {
					switch level {
					case slog.LevelDebug:
						a.Value = slog.StringValue("DEBUG")
					case slog.LevelInfo:
						a.Value = slog.StringValue("INFO")
					case slog.LevelWarn:
						a.Value = slog.StringValue("WARNING")
					case slog.LevelError:
						a.Value = slog.StringValue("ERROR")
					default:
						a.Value = slog.StringValue("DEFAULT")
					}
				}
			case slog.TimeKey:
				a.Key = "timestamp"
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
		Level: slog.LevelDebug,
	}))
}
