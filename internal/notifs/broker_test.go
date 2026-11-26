package notifs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSendRacingCloseDoesNotPanic hammers Send from many goroutines while Close runs. The broker
// used to close its input channel when draining, so a Send that passed the closed check just before
// Close could panic with "send on closed channel" (about 1 in 100 shutdowns under this load).
func TestSendRacingCloseDoesNotPanic(t *testing.T) {
	const (
		rounds  = 2000
		senders = 8
	)

	for i := range rounds {
		b := NewNotificationBroker(4)
		go func() {
			for range b.Output() {
			}
		}()

		var (
			wg       sync.WaitGroup
			panicked atomic.Value
		)
		start := make(chan struct{})
		for range senders {
			wg.Go(func() {
				defer func() {
					if r := recover(); r != nil {
						panicked.Store(r)
					}
				}()
				<-start
				deadline := time.Now().Add(200 * time.Microsecond)
				for time.Now().Before(deadline) {
					b.Send(context.Background(), RecoverableRedisNotification{Type: StreamAdded})
				}
			})
		}

		close(start)
		time.Sleep(20 * time.Microsecond)
		b.Close()
		wg.Wait()

		if r := panicked.Load(); r != nil {
			t.Fatalf("round %d: Send panicked while racing Close: %v", i, r)
		}
	}
}

// TestSendAfterCloseIsRejected checks that Send reports false once the broker is closed, and that
// everything accepted before Close is delivered before the output channel closes.
func TestSendAfterCloseIsRejected(t *testing.T) {
	b := NewNotificationBroker(4)

	for range 3 {
		if !b.Send(context.Background(), RecoverableRedisNotification{Type: StreamAdded}) {
			t.Fatal("Send before Close returned false")
		}
	}

	b.Close()

	if b.Send(context.Background(), RecoverableRedisNotification{Type: StreamAdded}) {
		t.Fatal("Send after Close returned true")
	}

	got := 0
	for range b.Output() {
		got++
	}
	if got != 3 {
		t.Fatalf("delivered %d notifications, want 3", got)
	}
}
