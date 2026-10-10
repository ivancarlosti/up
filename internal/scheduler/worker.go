package scheduler

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// worker runs the probe of a single monitor.
//
// The first execution happens immediately (with a small random jitter so a
// restart does not fire every monitor at the same instant) and every following
// execution is scheduled by a ticker holding the current interval.
type worker struct {
	id        uint
	scheduler *Scheduler
	trigger   chan struct{}

	mu      sync.RWMutex
	monitor *models.Monitor
	// family is the address family of the NEXT execution of an alternating
	// monitor. The rotation is in memory on purpose: it is a property of this
	// worker (not of the monitor row), it re-seeds on restart, and it never needs
	// to be visible to another node.
	family string
	cancel context.CancelFunc
}

// run is the worker main loop.
func (w *worker) run(ctx context.Context) {
	timer := time.NewTimer(jitter())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// A reload may have changed the interval in the meantime.
			if w.executeOnce(ctx, false) {
				// A "check now" arrived while the retry backoff was sleeping: it
				// broke the wait (instead of queueing behind it), so the owed
				// manual check runs right here as a single attempt.
				w.executeOnce(ctx, true)
			}
			timer.Reset(w.interval())
		case <-w.trigger:
			// Only reachable while the worker is idle: w.trigger is also the
			// interrupt an in-flight backoff selects on, and this branch cannot
			// run at the same time as that execution.
			w.executeOnce(ctx, true)
		}
	}
}

// stop cancels the worker.
func (w *worker) stop() { w.cancel() }

// triggerNow asks for an immediate execution.
func (w *worker) triggerNow() {
	select {
	case w.trigger <- struct{}{}:
	default:
	}
}

// update replaces the monitor configuration used by the next execution.
func (w *worker) update(monitor *models.Monitor) {
	w.mu.Lock()
	w.monitor = monitor
	w.mu.Unlock()
}

// current returns a snapshot of the monitor configuration.
func (w *worker) current() *models.Monitor {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.monitor
}

// interval is the current ticker duration.
func (w *worker) interval() time.Duration {
	monitor := w.current()
	seconds := 60
	if monitor != nil && monitor.IntervalSeconds > 0 {
		seconds = monitor.IntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

// executeOnce reloads the monitor state and runs a single execution.
//
// singleShot marks a manual "check now", which bypasses the retry policy so the
// operator gets a heartbeat immediately. It returns true when a check-now broke
// an in-flight retry backoff (the caller then owes an immediate single-shot run).
func (w *worker) executeOnce(ctx context.Context, singleShot bool) bool {
	monitor := w.current()
	if monitor == nil {
		return false
	}
	// Reload from the database so a paused monitor stops immediately and a
	// changed configuration takes effect without restarting the worker.
	fresh, err := w.scheduler.monitors.Get(ctx, monitor.ID)
	if err != nil {
		w.scheduler.log.Warn("could not reload the monitor before checking",
			"monitor_id", monitor.ID, "error", err)
		return false
	}
	if !fresh.Active {
		return false
	}
	w.update(fresh)
	return w.scheduler.execute(ctx, fresh, w.probeFamily(fresh), singleShot, w.trigger)
}

// probeFamily returns the address family of this execution and advances the
// rotation.
//
// Only a monitor configured with `alternate` rotates: it consumes a turn on every
// execution and the checkers then insist (softly) on that family. Any other
// preference gets an empty turn, so the checkers honor its explicit
// `ipv4`/`ipv6` pin - or the plain happy eyeballs of `auto`, which is the
// default and the behavior of a monitor stored before the field existed.
// The state flips whether the execution succeeds or fails, so a host with one
// dead family is reported down on the turns that test it instead of looking
// healthy because the other family answered.
func (w *worker) probeFamily(monitor *models.Monitor) string {
	if models.NormalizeIPFamily(monitor.Config.IPFamily) != models.IPFamilyAlternate {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	family := w.family
	w.family = models.NextIPFamily(family)
	return family
}

// jitter spreads the first execution of the workers over a few seconds.
func jitter() time.Duration {
	return time.Duration(rand.Intn(3000)) * time.Millisecond
}

// seedFamily is the first turn of a worker: odd monitor ids start on IPv6 and
// even ones on IPv4. The split means a restart does not put every alternating
// monitor on the same family at the same second, which would double the load of
// one path (and, on a host whose IPv6 route is down, make every monitor look down
// at once instead of half of them).
func seedFamily(monitorID uint) string {
	if monitorID%2 == 0 {
		return models.IPFamily4
	}
	return models.IPFamily6
}
