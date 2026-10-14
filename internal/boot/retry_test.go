package boot

import (
	"testing"
	"time"
)

// TestRetryWakesTheWaitLoop pins the whole contract of the signal: it is silent
// until it is asked, it wakes a wait that is selecting on it, and one wake up is
// enough (the loop goes back to waiting on its timer afterwards).
func TestRetryWakesTheWaitLoop(t *testing.T) {
	retry := NewRetry()

	select {
	case <-retry.Chan():
		t.Fatal("the signal fired before Now() was called")
	default:
	}

	retry.Now()

	select {
	case <-retry.Chan():
	case <-time.After(time.Second):
		t.Fatal("Now() did not wake the wait loop")
	}

	select {
	case <-retry.Chan():
		t.Fatal("one Now() must produce exactly one wake up")
	default:
	}
}

// TestRetryNowNeverBlocks guards the HTTP handler that calls it: a burst of
// clicks (or a client retrying the endpoint) must never block the request, and
// the pending requests must collapse into the single retry they mean.
func TestRetryNowNeverBlocks(t *testing.T) {
	retry := NewRetry()
	done := make(chan struct{})
	go func() {
		// Far more calls than the buffer can hold.
		for i := 0; i < 1000; i++ {
			retry.Now()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Now() blocked")
	}

	// Only one wake up is queued, however many requests arrived.
	<-retry.Chan()
	select {
	case <-retry.Chan():
		t.Fatal("the requests were queued individually instead of collapsed")
	default:
	}
}

// TestNilRetryTolerated documents the nil receiver contract, the same one State
// keeps: a caller that does not offer the button builds no Retry, and the wait
// loop only wakes up on its own timer because a nil channel blocks forever.
func TestNilRetryTolerated(t *testing.T) {
	var retry *Retry
	retry.Now() // must not panic
	if retry.Chan() != nil {
		t.Fatal("a nil Retry must expose a nil channel")
	}
}
