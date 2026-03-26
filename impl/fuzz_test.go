package impl

import (
	"testing"
	"time"
)

// FuzzExtractStreamnameFromKspChannel fuzzes the ksp channel string extraction logic.
func FuzzExtractStreamnameFromKspChannel(f *testing.F) {
	f.Add("__keyspace@0__:my-stream-input")
	f.Add("__keyspace@0__:")
	f.Add("")
	f.Add("invalid-prefix:stream")
	f.Add("__keyspace@0__:stream-with-special-chars!@#$%^&*()")
	f.Add("__keyspace@0__:a")

	r := &RecoverableRedisStreamClient{}
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = r.extractStreamnameFromKspChannel(input) //nolint:errcheck
	})
}

// FuzzRetryConfigValidate fuzzes the retry configuration validation logic.
func FuzzRetryConfigValidate(f *testing.F) {
	f.Add(-1, int64(100*time.Millisecond), int64(30*time.Second))
	f.Add(0, int64(100*time.Millisecond), int64(30*time.Second))
	f.Add(5, int64(100*time.Millisecond), int64(30*time.Second))
	f.Add(-2, int64(100*time.Millisecond), int64(30*time.Second))
	f.Add(5, int64(0), int64(30*time.Second))
	f.Add(5, int64(100*time.Millisecond), int64(50*time.Millisecond))

	f.Fuzz(func(t *testing.T, maxRetries int, initialDelayNs, maxDelayNs int64) {
		rc := RetryConfig{
			MaxRetries:        maxRetries,
			InitialRetryDelay: time.Duration(initialDelayNs),
			MaxRetryDelay:     time.Duration(maxDelayNs),
		}
		_ = rc.Validate() //nolint:errcheck
	})
}
