package scheduler

import (
	"testing"

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
