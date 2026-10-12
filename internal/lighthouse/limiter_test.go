package lighthouse

import (
	"context"
	"testing"
	"time"
)

// TestLimiterPacesTheCalls pins the pacing rule: the second call of the same
// host waits at least the interval, a zero interval never waits and a cancelled
// context stops the wait.
func TestLimiterPacesTheCalls(t *testing.T) {
	limiter := NewLimiter()
	ctx := context.Background()
	if err := limiter.Wait(ctx, "googleapis.com", 0); err != nil {
		t.Fatalf("a zero interval must not wait: %v", err)
	}

	interval := 80 * time.Millisecond
	started := time.Now()
	if err := limiter.Wait(ctx, "googleapis.com", interval); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if err := limiter.Wait(ctx, "googleapis.com", interval); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed < interval {
		t.Fatalf("the second call was not paced (%s)", elapsed)
	}

	// A different host has its own slot.
	other := time.Now()
	if err := limiter.Wait(ctx, "example.com", interval); err != nil {
		t.Fatalf("other host: %v", err)
	}
	if elapsed := time.Since(other); elapsed >= interval {
		t.Fatalf("the hosts must not share a slot (%s)", elapsed)
	}
}

// TestLimiterHonoursTheContext checks that a cancelled context interrupts the
// wait instead of blocking the job.
func TestLimiterHonoursTheContext(t *testing.T) {
	limiter := NewLimiter()
	if err := limiter.Wait(context.Background(), "googleapis.com", time.Minute); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := limiter.Wait(ctx, "googleapis.com", time.Minute); err == nil {
		t.Fatal("a cancelled context must stop the wait")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the wait was not interrupted (%s)", elapsed)
	}
}
