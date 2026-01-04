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
	if len(parts) == 1 || len(parts) > 2 {
		return LBSInfo{}, fmt.Errorf("invalid ksp format: must be datastream_name<MUTEX_KEY_SEP>message_id_in_lbs")
	}

	return LBSInfo{
		DataStreamName: parts[0],
		IDInLBS:        parts[1],
	}, nil
}

func CreateByParts(dataStreamName string, idInLBS string) (LBSInfo, error) {
	if len(dataStreamName) == 0 || len(idInLBS) == 0 {
		return LBSInfo{}, fmt.Errorf("malformed or incomplete LBS message")
	}

	return LBSInfo{
		DataStreamName: dataStreamName,
		IDInLBS:        idInLBS,
	}, nil
}
