package notifs

import (
	"fmt"
	"strings"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
)

// LBSInfo contains information about a data stream in LBS. It's an internal data structure.
type LBSInfo struct {
	DataStreamName string
	IDInLBS        string
}

func (l *LBSInfo) FormMutexKey() string {
	return strings.Join([]string{l.DataStreamName, l.IDInLBS}, configs.MutexKeySep)
}

func CreateByKspNotification(mutexKey string) (LBSInfo, error) {
	parts := strings.Split(mutexKey, configs.MutexKeySep)
	// must be in format: data_stream_name:message_id_in_lbs
	if len(parts) == 1 || len(parts) > 2 {
		return LBSInfo{}, fmt.Errorf("invalid mutex key format: %s", mutexKey)
	}

	return LBSInfo{
		DataStreamName: parts[0],
		IDInLBS:        parts[1],
	}, nil
}

func CreateByParts(dataStreamName string, idInLBS string) (LBSInfo, error) {
	if len(dataStreamName) == 0 || len(idInLBS) == 0 {
		return LBSInfo{}, fmt.Errorf("no data to create lbsInfo")
	}

	return LBSInfo{
		DataStreamName: dataStreamName,
		IDInLBS:        idInLBS,
	}, nil
}
