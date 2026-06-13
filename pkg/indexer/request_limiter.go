package indexer

import (
	"context"
	"sync"
	"time"
)

// RequestLimiter serializes requests to an indexer to a maximum average rate.
// A zero or negative rate means "disabled".
type RequestLimiter struct {
	interval    time.Duration
	nextAllowed time.Time
	mu          sync.Mutex
}

func NewRequestLimiter(rps int) *RequestLimiter {
	if rps <= 0 {
		return nil
	}
	return &RequestLimiter{
		interval: time.Second / time.Duration(rps),
	}
}

func (l *RequestLimiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	now := time.Now()

	l.mu.Lock()
	if l.nextAllowed.Before(now) {
		l.nextAllowed = now
	}
	prevNext := l.nextAllowed
	wait := l.nextAllowed.Sub(now)
	l.nextAllowed = l.nextAllowed.Add(l.interval)
	if wait <= 0 {
		if err := ctx.Err(); err != nil {
			l.nextAllowed = prevNext
			l.mu.Unlock()
			return err
		}
		l.mu.Unlock()
		return nil
	}
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		l.mu.Lock()
		l.nextAllowed = l.nextAllowed.Add(-l.interval)
		if now := time.Now(); l.nextAllowed.Before(now) {
			l.nextAllowed = now
		}
		l.mu.Unlock()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
