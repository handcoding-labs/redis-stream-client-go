package notifs

type NotificationType int

const (
	StreamAdded NotificationType = iota
	StreamDisowned
	StreamExpired
	General
)

type RecoverableRedisNotification[T any] struct {
	Type         NotificationType
	Notification T
}

// LBSMessage is the format in which the message should be written to LBS
type LBSMessage struct {
	DataStreamName string
	Info           map[string]interface{}
}

func Make(value any, notifType NotificationType) RecoverableRedisNotification[any] {
	return RecoverableRedisNotification[any]{
		Type:         notifType,
		Notification: value,
	}
}
