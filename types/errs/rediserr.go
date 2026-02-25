package errs

import "fmt"

type RedisOp string

const (
	OpEnableKeyspaceNotification RedisOp = "enabling keyspace notifications"
	OpGetUnackedMessages         RedisOp = "getting unacked messages from LBS"
	OpProcessLBSMessages         RedisOp = "processing LBS messages"
	OpCreateLBSStream            RedisOp = "creating LBS stream"
	OpClaimStream                RedisOp = "claiming stream"
	OpReadClaimedStream          RedisOp = "reading result from claimed stream"
	OpAckStream                  RedisOp = "acknowledging stream"
	OpClosePubSub                RedisOp = "closing redis pubsub"
)

func (op RedisOp) String() string {
	return string(op)
}

type RedisErr struct {
	Op  RedisOp
	Err error
}

func NewRedisError(op RedisOp, err error) error {
	return &RedisErr{Op: op, Err: err}
}

func (e *RedisErr) Error() string {
	return fmt.Sprintf("redis error during operation [%s]: %v", e.Op.String(), e.Err)
}

func (e *RedisErr) Unwwrap() error {
	return e.Err
}
