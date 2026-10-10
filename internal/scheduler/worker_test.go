package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/checkers"
	"github.com/ivancarlosti/up/internal/models"
)

// TestWorkerProbeFamily covers the rotation state of a worker: an alternating
// monitor consumes a turn on every execution and flips it, while a monitor with
// an explicit family (or auto) gets no turn at all - and, crucially, does not
// consume the state of the rotation.
func TestWorkerProbeFamily(t *testing.T) {
	rotating := &models.Monitor{Config: models.MonitorConfig{IPFamily: models.IPFamilyAlternate}}
	explicit := &models.Monitor{Config: models.MonitorConfig{IPFamily: models.IPFamily6}}
	pinned4 := &models.Monitor{Config: models.MonitorConfig{IPFamily: models.IPFamily4}}
	auto := &models.Monitor{Config: models.MonitorConfig{IPFamily: models.IPFamilyAuto}}

	w := &worker{family: models.IPFamily4}
	first := w.probeFamily(rotating)
	second := w.probeFamily(rotating)
	if first != models.IPFamily4 || second != models.IPFamily6 {
		t.Fatalf("the rotation must flip, got %q then %q", first, second)
	}

	// A monitor that does not rotate never gets a turn, whatever it asks for.
	for _, monitor := range []*models.Monitor{explicit, pinned4, auto} {
		if turn := w.probeFamily(monitor); turn != "" {
			t.Fatalf("%q must not receive a turn, got %q", monitor.Config.IPFamily, turn)
		}
	}

	// Those calls did not touch the state: the rotating monitor continues where
	// it stopped.
	if third := w.probeFamily(rotating); third != models.IPFamily4 {
		t.Fatalf("a non rotating monitor consumed the rotation: got %q, want %q", third, models.IPFamily4)
	}

	// A monitor stored before the field existed (empty) is `auto` too: the worker
	// decides it from the normalized value, so it does not rotate either.
	legacy := &models.Monitor{}
	if turn := w.probeFamily(legacy); turn != "" {
		t.Fatalf("a monitor without ip_family must not rotate (the default is auto), got %q", turn)
	}
}

// TestSeedFamily documents why the first turn is derived from the monitor id: a
// restart must not put every monitor on the same family at the same second, and
// each seeded worker still alternates afterwards.
func TestSeedFamily(t *testing.T) {
	if got := seedFamily(2); got != models.IPFamily4 {
		t.Fatalf("seedFamily(2) = %q, want %q", got, models.IPFamily4)
	}
	if got := seedFamily(3); got != models.IPFamily6 {
		t.Fatalf("seedFamily(3) = %q, want %q", got, models.IPFamily6)
	}
	// Two consecutive executions of any seeded worker cover both families.
	for _, id := range []uint{1, 2} {
		seed := seedFamily(id)
		if next := models.NextIPFamily(seed); next == seed {
			t.Fatalf("monitor %d never leaves %q", id, seed)
		}
	}
}

// TestProbeWithRetriesInterrupt covers the "check now" preemption: a trigger
// arriving while the retry backoff is sleeping must break the wait at once,
// instead of leaving the manual check queued behind the whole policy. Without
// the interrupt the minute-long wait would make this test time out - which is
// exactly the symptom of the button appearing to do nothing.
func TestProbeWithRetriesInterrupt(t *testing.T) {
	interrupt := make(chan struct{}, 1)
	probes := 0
	probe := func() checkers.Result {
		probes++
		return checkers.Result{Status: models.StatusDown}
	}

	type outcome struct {
		attempts    int
		interrupted bool
	}
	done := make(chan outcome, 1)
	go func() {
		_, attempts, interrupted := probeWithRetries(context.Background(), 3, time.Minute, interrupt, probe)
		done <- outcome{attempts: attempts, interrupted: interrupted}
	}()

	// If the goroutine has not reached the backoff yet the signal is buffered,
	// so the first select consumes it anyway: no race.
	time.Sleep(50 * time.Millisecond)
	interrupt <- struct{}{}

	select {
	case got := <-done:
		if !got.interrupted {
			t.Fatal("a trigger during the backoff must interrupt the retry loop")
		}
		if got.attempts != 0 {
			t.Fatalf("the closed retry must not probe again, got %d extra attempts", got.attempts)
		}
		if probes != 1 {
			t.Fatalf("only the initial probe must have run, got %d probes", probes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the trigger did not interrupt the backoff promptly")
	}
}

// TestProbeWithRetriesExhaustsBudget pins the un-interrupted policy: every retry
// runs, and a cancelled execution is not reported as interrupted (so the caller
// does not mistake a shutdown for a manual check).
func TestProbeWithRetriesExhaustsBudget(t *testing.T) {
	probes := 0
	_, attempts, interrupted := probeWithRetries(context.Background(), 2, time.Millisecond,
		make(chan struct{}),
		func() checkers.Result {
			probes++
			return checkers.Result{Status: models.StatusDown}
		})
	if interrupted || attempts != 2 || probes != 3 {
		t.Fatalf("the budget must be spent: interrupted=%v attempts=%d probes=%d", interrupted, attempts, probes)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, interrupted = probeWithRetries(cancelled, 3, time.Minute, make(chan struct{}),
		func() checkers.Result { return checkers.Result{Status: models.StatusDown} })
	if interrupted {
		t.Fatal("a cancelled execution is not an interrupted one: the caller must not run a manual check")
	}
}

// TestProbeWithRetriesStopsWhenUp documents that a healthy attempt ends the
// policy without spending the remaining budget.
func TestProbeWithRetriesStopsWhenUp(t *testing.T) {
	probes := 0
	result, attempts, interrupted := probeWithRetries(context.Background(), 5, time.Millisecond,
		make(chan struct{}),
		func() checkers.Result {
			probes++
			if probes == 2 {
				return checkers.Result{Status: models.StatusUp}
			}
			return checkers.Result{Status: models.StatusDown}
		})
	if interrupted || attempts != 1 || probes != 2 || result.Status != models.StatusUp {
		t.Fatalf("an up retry must stop the policy: interrupted=%v attempts=%d probes=%d status=%v",
			interrupted, attempts, probes, result.Status)
	}
}

// TestVerdictIsImportant pins which heartbeats may open or close an incident. A
// manual "check now" that bypassed a retry policy must not: its single failing
// probe would alert while the monitor's own policy would still be retrying.
func TestVerdictIsImportant(t *testing.T) {
	cases := []struct {
		name       string
		singleShot bool
		retries    int
		want       bool
	}{
		{"scheduled execution with retries", false, 5, true},
		{"scheduled execution without retries", false, 0, true},
		{"manual check bypassing retries", true, 5, false},
		{"manual check without retries", true, 0, true},
	}
	for _, tc := range cases {
		if got := verdictIsImportant(tc.singleShot, tc.retries); got != tc.want {
			t.Errorf("%s: verdictIsImportant(%v, %d) = %v, want %v",
				tc.name, tc.singleShot, tc.retries, got, tc.want)
		}
	}
}
