package expiry

import (
	"context"
	"sync"
	"time"
)

// Limiter spaces the outbound registry lookups.
//
// It is deliberately tiny and dependency free (the same spirit as the
// middleware rate limiter): registry servers tolerate a slow crawler and punish
// a burst, and the operator picks the pace on the expiry settings page. The
// limiter is keyed by the registry host, so a slow WHOIS server does not slow
// down the RDAP lookups against a different one.
type Limiter struct {
	mu   sync.Mutex
	next map[string]time.Time
}

// NewLimiter builds an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{next: map[string]time.Time{}}
}

// Wait blocks until a lookup against `key` is allowed again.
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
