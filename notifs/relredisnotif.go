package notifs

// Types of notifications sent to client.
type NotificationType int

const (
	Unknown NotificationType = iota
	StreamAdded
	StreamDisowned
	StreamExpired
	StreamTerminated
)

// RecoverableRedisNotification captures the type of notifications sent to client.
// These are captured by NotificationType enum.
type RecoverableRedisNotification struct {
	Type    NotificationType
	Payload LBSInfo
	// AdditionalInfo is an echo from any additional data seeded in LBSInputMessage
	AdditionalInfo map[string]any
}

func (n RecoverableRedisNotification) IsZero() bool {
	return n.Type == Unknown
}

// LBSMessage is the format in which the message should be written to LBS
type LBSInputMessage struct {
	DataStreamName string
	Info           map[string]interface{}
}

func Make(notifType NotificationType, lbsInfo LBSInfo, additionalInfo map[string]any) RecoverableRedisNotification {
	return RecoverableRedisNotification{
		Type:           notifType,
		Payload:        lbsInfo,
		AdditionalInfo: additionalInfo,
	}
}

func MakeStreamTerminatedNotif(info string) RecoverableRedisNotification {
	return RecoverableRedisNotification{
		Type:    StreamTerminated,
		Payload: LBSInfo{},
		AdditionalInfo: map[string]any{
			"info": info,
		},
	}
}
