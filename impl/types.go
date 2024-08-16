package impl

import (
	"bburli/redis-stream-client-go/types"
	"fmt"
	"strings"

	"github.com/go-redsync/redsync/v4"
)

// lbsInfo contains information about a data stream in LBS. It's an internal data structure.
type lbsInfo struct {
	DataStreamName string
	IDInLBS        string
	Mutex          *redsync.Mutex
}

func (l *lbsInfo) getMutexKey() string {
	return strings.Join([]string{l.DataStreamName, l.IDInLBS}, types.MutexKeySep)
}

func createByMutexKey(mutexKey string) (*lbsInfo, error) {
	parts := strings.Split(mutexKey, types.MutexKeySep)
	// must be in format: data_stream_name:message_id_in_lbs
	if len(parts) == 1 || len(parts) > 2 {
		return nil, fmt.Errorf("invalid mutex key format: ", mutexKey)
	}

	return &lbsInfo{
		DataStreamName: parts[0],
		IDInLBS:        parts[1],
	}, nil
}

func createByParts(dataStreamName string, idInLBS string) (*lbsInfo, error) {
	if len(dataStreamName) == 0 || len(idInLBS) == 0 {
		return nil, fmt.Errorf("no data to create lbsInfo")
	}

	return &lbsInfo{
		DataStreamName: dataStreamName,
		IDInLBS:        idInLBS,
	}, nil
}
