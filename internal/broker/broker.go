// Package broker serializes notifications from the client's goroutines onto the single output
// channel returned by Init. It is internal so that only the client can create, feed and close it.
package broker

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/handcoding-labs/redis-stream-client-go/notifs"
)

type NotificationBroker struct {
	input     chan notifs.RecoverableRedisNotification
	output    chan notifs.RecoverableRedisNotification
	wg        *sync.WaitGroup
	closed    atomic.Bool
	quit      chan struct{}
	closeOnce sync.Once
}

func NewNotificationBroker(bufferSize int) *NotificationBroker {
	b := &NotificationBroker{
		input:  make(chan notifs.RecoverableRedisNotification, bufferSize),
		output: make(chan notifs.RecoverableRedisNotification, bufferSize),
		wg:     &sync.WaitGroup{},
		quit:   make(chan struct{}),
	}

	b.wg.Add(1)
	go b.run()
	return b
}

func (b *NotificationBroker) run() {
	defer b.wg.Done()

	for {
		select {
		case <-b.quit:
			// drain
			for {
				select {
				case m, ok := <-b.input:
					if ok {
						b.output <- m
					}
				default:
					// input is deliberately never closed: it has many senders, and a Send that
					// passed its closed check just before Close would otherwise panic with
					// "send on closed channel". Such a late Send either sees quit or lands in
					// the buffer and is dropped; the channel is garbage collected with the broker.
					return
				}
			}
		case m, ok := <-b.input:
			if ok {
				b.output <- m
			}
		}
	}
}

func (b *NotificationBroker) Send(ctx context.Context, m notifs.RecoverableRedisNotification) bool {
	if b.closed.Load() {
		return false
	}

	select {
	case <-ctx.Done():
		return false
	case <-b.quit:
		return false
	case b.input <- m:
		return true
	}
}

// StopAccepting makes every later Send return false without closing anything. Done calls it before
// canceling the senders, so goroutines exiting because of a caller-initiated shutdown don't emit
// StreamTerminated notifications. Notifications already accepted are still delivered by Close.
func (b *NotificationBroker) StopAccepting() {
	b.closed.Store(true)
}

// Close stops accepting notifications, delivers everything already accepted, and closes the
// output channel. It is safe to call more than once and from several goroutines: later calls
// wait for the first one to finish and then return without closing anything again.
func (b *NotificationBroker) Close() {
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		close(b.quit)

		// wait for drain
		b.wg.Wait()

		close(b.output)
	})
}

func (b *NotificationBroker) Output() <-chan notifs.RecoverableRedisNotification {
	return b.output
}
