package impl

import (
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/configs"
)

// TestOutputChannelBufferedByDefault checks that without WithOutputChanSize the output channel
// keeps the default buffer instead of silently becoming unbuffered.
func TestOutputChannelBufferedByDefault(t *testing.T) {
	t.Setenv(configs.PodName, "default-buffer")

	rc := redis.NewClient(&redis.Options{Addr: "localhost:0"})
	defer rc.Close()

	c, err := NewRedisStreamClient(rc, "default-buffer")
	if err != nil {
		t.Fatal(err)
	}

	r := c.(*RecoverableRedisStreamClient)
	defer r.notificationBroker.Close()

	if got := cap(r.notificationBroker.Output()); got != configs.DefaultOutputChanSize {
		t.Fatalf("output channel capacity = %d, want %d", got, configs.DefaultOutputChanSize)
	}
}
