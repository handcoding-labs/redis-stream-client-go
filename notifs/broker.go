package notifs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var (
	ErrContextDone   = errors.New("context is done")
	ErrAlreadyClosed = errors.New("output already closed")
)

type NotificationBroker struct {
	input     chan RecoverableRedisNotification
	output    chan<- RecoverableRedisNotification
	wg        *sync.WaitGroup
	closed    atomic.Bool
	closeOnce sync.Once
}

func NewNotificationBroker(output chan<- RecoverableRedisNotification, bufferSize int) *NotificationBroker {
	b := &NotificationBroker{
		input:  make(chan RecoverableRedisNotification, bufferSize),
		output: output,
		wg:     &sync.WaitGroup{},
	}

	b.wg.Add(1)
	go b.run()
	return b
}

func (b *NotificationBroker) run() {
	defer b.wg.Done()
	for m := range b.input {
		b.output <- m
	}
}

func (b *NotificationBroker) Send(ctx context.Context, m RecoverableRedisNotification) error {
	if b.closed.Load() {
		return fmt.Errorf("output chan closed")
	}

	select {
	case <-ctx.Done():
		return ErrContextDone
	case b.input <- m:
		return nil
	}
}

func (b *NotificationBroker) Close() {
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		close(b.input)
	})
}

func (b *NotificationBroker) Wait() {
	b.wg.Wait()
}
