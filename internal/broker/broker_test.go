package broker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/handcoding-labs/redis-stream-client-go/notifs"
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
					b.Send(context.Background(), notifs.RecoverableRedisNotification{Type: notifs.StreamAdded})
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
		if !b.Send(context.Background(), notifs.RecoverableRedisNotification{Type: notifs.StreamAdded}) {
			t.Fatal("Send before Close returned false")
		}
	}

	b.Close()

	if b.Send(context.Background(), notifs.RecoverableRedisNotification{Type: notifs.StreamAdded}) {
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

// TestCloseIsIdempotent checks that Close can be called again, including concurrently, without
// panicking on a second close of the output channel.
func TestCloseIsIdempotent(t *testing.T) {
	b := NewNotificationBroker(4)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("concurrent Close panicked: %v", r)
				}
			}()
			b.Close()
		})
	}
	wg.Wait()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Close after Close panicked: %v", r)
		}
	}()
	b.Close()

	if _, ok := <-b.Output(); ok {
		t.Fatal("output channel still open after Close")
	}
}

// TestStopAcceptingRejectsNewSendsButKeepsAccepted checks that after StopAccepting, Send is
// rejected while notifications accepted earlier are still delivered by Close.
func TestStopAcceptingRejectsNewSendsButKeepsAccepted(t *testing.T) {
	b := NewNotificationBroker(4)

	if !b.Send(context.Background(), notifs.RecoverableRedisNotification{Type: notifs.StreamAdded}) {
		t.Fatal("Send before StopAccepting returned false")
	}

	b.StopAccepting()

	if b.Send(context.Background(), notifs.MakeStreamTerminatedNotif("context done")) {
		t.Fatal("Send after StopAccepting returned true")
	}

	b.Close()

	var got []notifs.NotificationType
	for n := range b.Output() {
		got = append(got, n.Type)
	}
	if len(got) != 1 || got[0] != notifs.StreamAdded {
		t.Fatalf("delivered %v, want only the StreamAdded accepted before StopAccepting", got)
	}
}
