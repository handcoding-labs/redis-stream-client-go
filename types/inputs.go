package types

// LBSMessage is the format in which the message should be written to LBS
type LBSMessage struct {
	DataStreamName string
	Info           map[string]interface{}
}
