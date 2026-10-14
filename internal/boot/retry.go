package boot

// Retry is the "try now" signal of the boot sequence.
//
// The boot retries on its own schedule - database.Connect every second up to
// eight, the heavier steps with the capped backoff of cmd/server - but a
// schedule cannot know the one thing that matters: the operator has just fixed
// the DSN or the identity provider and does not want to wait for the next tick.
// Retry is that extra wake up. The bootstrap engine calls Now() when the boot
// screen asks, and every wait loop of the boot selects on Chan() next to its
// timer.
//
// Every method tolerates a nil receiver, like boot.State: a caller that does
// not offer the button (a test, a future command) passes nil and keeps a loop
// that only wakes up on its timer.
type Retry struct {
	kick chan struct{}
}

// NewRetry builds the retry signal.
func NewRetry() *Retry {
	// Buffered by one so Now() neither blocks nor loses a request that arrives
	// while the loop is busy failing: the loop drains it on its next wait.
	return &Retry{kick: make(chan struct{}, 1)}
}

// Now asks the boot sequence to retry immediately.
//
// It never blocks and never queues more than one request: the operator who
// clicks twice between two attempts means one retry, not two.
func (r *Retry) Now() {
	if r == nil {
		return
	}
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Chan is the receive side of the signal, meant to be selected on next to a
// timer.
//
// A nil *Retry returns a nil channel, which blocks forever: the wait loop then
// only wakes up on its own timer, which is what a nil signal means.
func (r *Retry) Chan() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.kick
}
