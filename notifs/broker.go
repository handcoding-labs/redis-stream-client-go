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
	output chan<- RecoverableRedisNotification
	wg     *sync.WaitGroup
	closed atomic.Bool
	quit   chan struct{}
}

func NewNotificationBroker(output chan<- RecoverableRedisNotification, bufferSize int) *NotificationBroker {
	b := &NotificationBroker{
		input:  make(chan RecoverableRedisNotification, bufferSize),
		output: output,
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
				case m := <-b.input:
					b.output <- m
				default:
					close(b.input)
					return
				}
			}
		case m := <-b.input:
			b.output <- m
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
