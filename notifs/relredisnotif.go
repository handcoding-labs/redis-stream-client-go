package notifs

type NotificationType int

const (
	StreamAdded NotificationType = iota
	StreamDisowned
	StreamExpired
	General
)

type RelRedisNotification[T any] struct {
	Type         NotificationType
	Notification T
}

type GenericNotification string

const (
	LBSChanClosed GenericNotification = "lbs_chan_closed"
	KspChanClosed GenericNotification = "ksp_chan_closed"
)

// LBSMessage is the format in which the message should be written to LBS
type LBSMessage struct {
	DataStreamName string
	Info           map[string]interface{}
}
