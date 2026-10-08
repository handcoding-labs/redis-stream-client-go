package errs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Go's errors.Is, errors.As and errors.Unwrap only follow a method named exactly Unwrap. These tests
// pin that every error type in this package that wraps a cause exposes it that way.

func TestRedisErrExposesItsCause(t *testing.T) {
	err := NewRedisError(OpReQueue, context.Canceled)

	require.True(t, errors.Is(err, context.Canceled), "errors.Is must see the cause of a RedisErr")
	require.Equal(t, context.Canceled, errors.Unwrap(err))

	var redisErr *RedisErr
	require.True(t, errors.As(err, &redisErr))
	require.Equal(t, OpReQueue, redisErr.Op)
}

func TestMutexErrExposesItsCause(t *testing.T) {
	cause := errors.New("lock already expired")
	err := NewMutexError(OpUnlockMutex, cause)

	require.True(t, errors.Is(err, cause), "errors.Is must see the cause of a MutexErr")
	require.Equal(t, cause, errors.Unwrap(err))

	var mutexErr *MutexErr
	require.True(t, errors.As(err, &mutexErr))
	require.Equal(t, OpUnlockMutex, mutexErr.Op)
}

func TestSentinelsSurviveWrappingByCallers(t *testing.T) {
	// a caller (or this library) that wraps a sentinel in a RedisErr can still match the sentinel
	err := NewRedisError(OpEnableKeyspaceNotification, ErrExistingConfigWithoutOverride)
	require.ErrorIs(t, err, ErrExistingConfigWithoutOverride)
}

func TestWrapperTypesImplementUnwrap(t *testing.T) {
	// guards against the method being misnamed again: errors.Is/As only follow `Unwrap() error`
	for _, v := range []any{&RedisErr{}, &MutexErr{}, &EncodingError{}} {
		_, ok := v.(interface{ Unwrap() error })
		require.True(t, ok, "%T must implement Unwrap() error", v)
	}
}
