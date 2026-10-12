package lighthouse

import (
	"context"
	"sync"
	"time"
)

// Limiter spaces the outbound PageSpeed Insights calls.
//
// It is deliberately tiny and dependency free (the same spirit as the registry
// limiter of the expiry package and of the middleware rate limiter): the public
// API funds its own quota and refuses a burst, and the operator picks the pace
// on the settings page. The limiter is keyed by host, so a future second
// endpoint never waits behind the first one.
type Limiter struct {
	mu   sync.Mutex
	next map[string]time.Time
}

// NewLimiter builds an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{next: map[string]time.Time{}}
}

// Wait blocks until a call to `key` is allowed again.
//
// The slot is reserved under the lock and the sleeping happens outside it, so
// concurrent callers are serialized per key without holding the mutex (and the
// context cancels a long wait).
func (l *Limiter) Wait(ctx context.Context, key string, interval time.Duration) error {
	if l == nil || interval <= 0 {
		return ctx.Err()
	}
	now := time.Now()
	l.mu.Lock()
	ready := l.next[key]
	if ready.Before(now) {
		ready = now
	}
	l.next[key] = ready.Add(interval)
	l.mu.Unlock()

	if wait := time.Until(ready); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
