package errs

import "fmt"

type EncodingOp string

const (
	OpUnmarshalLBSMessage = "unmarshalling LBS message"
)

func (op EncodingOp) String() string {
	return string(op)
}

type EncodingError struct {
	Op  EncodingOp
	Err error
}

func NewEncodingError(op EncodingOp, err error) error {
	return &EncodingError{Op: op, Err: err}
}

func (e *EncodingError) Error() string {
	return fmt.Sprintf("encoding error on operation [%s]: %v", e.Op.String(), e.Err)
}

func (e *EncodingError) Unwrap() error {
	return e.Err
}
