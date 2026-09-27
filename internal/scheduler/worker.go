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
			w.executeOnce(ctx)
			timer.Reset(w.interval())
		case <-w.trigger:
			w.executeOnce(ctx)
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

// executeOnce reloads the monitor state and runs a single probe.
func (w *worker) executeOnce(ctx context.Context) {
	monitor := w.current()
	if monitor == nil {
		return
	}
	// Reload from the database so a paused monitor stops immediately and a
	// changed configuration takes effect without restarting the worker.
	fresh, err := w.scheduler.monitors.Get(ctx, monitor.ID)
	if err != nil {
		w.scheduler.log.Warn("could not reload the monitor before checking",
			"monitor_id", monitor.ID, "error", err)
		return
	}
	if !fresh.Active {
		return
	}
	w.update(fresh)
	w.scheduler.execute(ctx, fresh, w.probeFamily(fresh))
}

// probeFamily returns the address family of this execution and advances the
// rotation.
//
// A monitor that is not configured with `alternate` has no rotation at all: it
// gets an empty turn, so the checkers honor its explicit `ipv4`/`ipv6` (or the
// plain happy eyeballs of `auto`) on every execution. Only `alternate` consumes
// the state, and the state flips whether the execution succeeds or fails, so a
// host with one dead family is reported down on the turns that test it instead of
// looking healthy because the other family answered.
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
