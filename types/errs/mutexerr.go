package errs

import "fmt"

type MutexOp string

const (
	OpLockMutex   MutexOp = "locking mutex"
	OpExtendMutex MutexOp = "extending mutex lock"
	OpUnlockMutex MutexOp = "unlocking mutex"
)

func (op MutexOp) String() string {
	return string(op)
}

type MutexErr struct {
	Op       MutexOp
	LockName string
	Err      error
}

func NewMutexError(op MutexOp, err error) error {
	return &MutexErr{Op: op, Err: err}
}

func (e *MutexErr) Error() string {
	if e.LockName != "" {
		return fmt.Sprintf("mutex error for lock [%s] on operation [%s]: %v", e.LockName, e.Op.String(), e.Err)
	}

	return fmt.Sprintf("mutex error on operation [%s]: %v", e.Op.String(), e.Err)
}

func (e *MutexErr) Unwwrap() error {
	return e.Err
}
