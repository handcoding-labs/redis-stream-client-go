package notifs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	ErrContextDone   = errors.New("context is done")
	ErrAlreadyClosed = errors.New("output already closed")
)

type NotificationBroker struct {
	input  chan RecoverableRedisNotification
	output chan RecoverableRedisNotification
	wg     *sync.WaitGroup
	closed atomic.Bool
	quit   chan struct{}
}

func NewNotificationBroker(bufferSize int) *NotificationBroker {
	b := &NotificationBroker{
		input:  make(chan RecoverableRedisNotification, bufferSize),
		output: make(chan RecoverableRedisNotification, bufferSize),
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

func (b *NotificationBroker) Send(ctx context.Context, m RecoverableRedisNotification) bool {
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

func (b *NotificationBroker) Close() {
	// notify
	if b.closed.CompareAndSwap(false, true) {
		close(b.quit)
	}

	// wait for drain
	b.wg.Wait()

	// close output channel
	close(b.output)
}

func (b *NotificationBroker) Output() <-chan RecoverableRedisNotification {
	return b.output
}
