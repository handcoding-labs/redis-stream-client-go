package impl

import (
	"testing"
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
)

func TestParseRetryCount(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]interface{}
		want   int
	}{
		{"absent field defaults to 0", map[string]interface{}{configs.LBSInput: "x"}, 0},
		{"valid count", map[string]interface{}{configs.RetryCountField: "3"}, 3},
		{"zero", map[string]interface{}{configs.RetryCountField: "0"}, 0},
		{"non-string defaults to 0", map[string]interface{}{configs.RetryCountField: 4}, 0},
		{"malformed defaults to 0", map[string]interface{}{configs.RetryCountField: "abc"}, 0},
		{"negative defaults to 0", map[string]interface{}{configs.RetryCountField: "-2"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (lbsEntry{values: tc.values}).retryCount(); got != tc.want {
				t.Fatalf("retryCount(%v) = %d, want %d", tc.values, got, tc.want)
			}
		})
	}
}

func TestCloneValuesIsIndependentCopy(t *testing.T) {
	orig := map[string]interface{}{
		configs.LBSInput:        `{"DataStreamName":"s1"}`,
		configs.RetryCountField: "1",
	}

	clone := (lbsEntry{values: orig}).cloneValues()
	clone[configs.RetryCountField] = "2"
	clone["new"] = "v"

	if orig[configs.RetryCountField] != "1" {
		t.Fatalf("mutating clone changed original retry count: %v", orig[configs.RetryCountField])
	}
	if _, ok := orig["new"]; ok {
		t.Fatalf("mutating clone leaked a new key into the original")
	}
	if clone[configs.LBSInput] != orig[configs.LBSInput] {
		t.Fatalf("clone did not preserve lbs-input payload")
	}
}

func TestDataStreamNameFromValues(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		name, err := (lbsEntry{values: map[string]interface{}{
			configs.LBSInput: `{"DataStreamName":"stream-42","Info":{"k":"v"}}`,
		}}).dataStreamName()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "stream-42" {
			t.Fatalf("got %q, want stream-42", name)
		}
	})

	t.Run("missing lbs-input key", func(t *testing.T) {
		if _, err := (lbsEntry{values: map[string]interface{}{"other": "x"}}).dataStreamName(); err == nil {
			t.Fatal("expected error for missing lbs-input field")
		}
	})

	t.Run("empty data stream name", func(t *testing.T) {
		if _, err := (lbsEntry{values: map[string]interface{}{
			configs.LBSInput: `{"DataStreamName":""}`,
		}}).dataStreamName(); err == nil {
			t.Fatal("expected error for empty data stream name")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		if _, err := (lbsEntry{values: map[string]interface{}{
			configs.LBSInput: `{not-json`,
		}}).dataStreamName(); err == nil {
			t.Fatal("expected error for malformed json")
		}
	})
}

func TestNextReconciliationDelay(t *testing.T) {
	base := 60 * time.Second
	maxExpected := base + time.Duration(float64(base)*configs.DefaultJitterFraction)

	for i := 0; i < 1000; i++ {
		d := nextReconciliationDelay(base)
		if d < base || d >= maxExpected {
			t.Fatalf("delay %v out of range [%v, %v)", d, base, maxExpected)
		}
	}

	// non-positive interval falls back to the default and never panics
	if d := nextReconciliationDelay(0); d < configs.DefaultReconciliationInterval {
		t.Fatalf("zero interval should fall back to default, got %v", d)
	}
}

func TestRecoveryConfigValidate(t *testing.T) {
	if err := DefaultRecoveryConfig().Validate(); err != nil {
		t.Fatalf("default recovery config should be valid: %v", err)
	}

	bad := []RecoveryConfig{
		{ReconciliationInterval: 0, MinIdleTime: time.Second, BatchSize: 1, MaxRetries: 0},
		{ReconciliationInterval: time.Second, MinIdleTime: 0, BatchSize: 1, MaxRetries: 0},
		{ReconciliationInterval: time.Second, MinIdleTime: time.Second, BatchSize: 0, MaxRetries: 0},
		{ReconciliationInterval: time.Second, MinIdleTime: time.Second, BatchSize: 1, MaxRetries: -1},
	}
	for i, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("config %d expected to be invalid", i)
		}
	}
}
