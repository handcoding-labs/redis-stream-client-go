package impl

import (
	"encoding/json"
	"strconv"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
	"github.com/handcoding-labs/redis-stream-client-go/notifs"
	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// lbsEntry is a thin wrapper around a single LBS stream entry — its ID and raw field/value map.
// It centralizes the parsing and cloning logic used by the recovery path so the knowledge of the
// internal field names (lbs-input, _retry_count, _dlq_reason) lives in one place.
type lbsEntry struct {
	id     string
	values map[string]interface{}
}

// retryCount reads the _retry_count field, defaulting to 0 when the field is absent or malformed
// (e.g. fresh messages produced by clients that are unaware of the field).
func (e lbsEntry) retryCount() int {
	v, ok := e.values[configs.RetryCountField]
	if !ok {
		return 0
	}
	s, ok := v.(string)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// dataStreamName extracts the data stream name from the entry's lbs-input payload. It returns a
// typed error for each distinct malformed-message scenario (missing key, wrong type, bad JSON,
// empty stream name) so callers can handle them via the library's error framework.
func (e lbsEntry) dataStreamName() (string, error) {
	v, ok := e.values[configs.LBSInput]
	if !ok {
		return "", errs.ErrInvalidKeyForLBSMessage
	}
	s, ok := v.(string)
	if !ok {
		return "", errs.ErrInvalidLBSMessage
	}

	var msg notifs.LBSInputMessage
	if err := json.Unmarshal([]byte(s), &msg); err != nil {
		return "", errs.NewRedisError(errs.OpUnmarshalLBSMessage, err)
	}
	if msg.DataStreamName == "" {
		return "", errs.ErrNoDatastreamInLBSMessage
	}
	return msg.DataStreamName, nil
}

// cloneValues returns a shallow copy of the entry's field/value map so the original is never
// mutated when re-queuing or routing to the DLQ. Capacity leaves room for the two recovery fields
// the caller may add: configs.RetryCountField and configs.DLQReasonField.
func (e lbsEntry) cloneValues() map[string]interface{} {
	out := make(map[string]interface{}, len(e.values)+2)
	for k, v := range e.values {
		out[k] = v
	}
	return out
}
