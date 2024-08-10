package impl

import "github.com/go-redsync/redsync/v4"

// lbsInfo contains information about a data stream in LBS. It's an internal data structure.
type lbsInfo struct {
	DataStreamName string
	IDInLBS        string
	Mutex          *redsync.Mutex
}