package types

import "github.com/go-redsync/redsync/v4"

type LBSMessage struct {
	DataStreamName string
	Info           map[string]interface{}
}

type LBSInfo struct {
	DataStreamName string
	IDInLBS        string
	Mutex          *redsync.Mutex
}
