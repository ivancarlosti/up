// This file holds an OPT-IN integration check of what a monitor measured on
// "both" runs stores (LighthouseService.Record, ClearExcept, Get and History, see
// lighthouse.go and docs/monitors.md): one current row PER run, one history row
// per run with the run it came from, a result that arrives without a strategy
// filed with the default run instead of in a row nobody can label, and a monitor
// narrowed back to a single run keeping only that row.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database and it
// runs inside a transaction that is rolled back at the end. The usual way to run
// it:
//
//	docker run --rm -d --name up-lighthouse-verify -p 127.0.0.1:3309:3306 \
//	  -e MARIADB_ROOT_PASSWORD=root -e MARIADB_DATABASE=up mariadb:11
//	UP_SCRATCH_DSN='root:root@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestLighthouseBothRunsLive -v
//	docker rm -f up-lighthouse-verify
//
// Why it needs a real engine: the current row is written with an ON CONFLICT
// upsert whose identity is the primary key (monitor_id, strategy). Whether the
// desktop run lands on its own row or overwrites the mobile one is exactly what
// the engine's composite key decides, and the history row appended next to it is
// the second half of the same story.
package services

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// TestLighthouseBothRunsLive pins what a monitor measured on BOTH runs stores.
//
// It is the end-to-end counterpart of TestRunnerRunKeepsTheTargetStrategy: the
// runner stamps the strategy on the result it returns, and this is the table that
// proves the desktop run is no longer filed on the mobile row of the monitor
// (which is what made a "both" monitor show one score, the mobile one).
func TestLighthouseBothRunsLive(t *testing.T) {
	tx := scratchTx(t)
	ctx := context.Background()
	suffix := time.Now().UTC().Format("20060102150405.000000")
	cfg := &config.Config{NodeID: "verify"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitors := NewMonitorService(tx, cfg, log, nil)
	lighthouse := NewLighthouseService(tx, cfg, log)

	monitor := &models.Monitor{
		Name:            "zz-lighthouse-" + suffix,
		Type:            models.MonitorTypeHTTP,
		Active:          false,
		IntervalSeconds: 300,
		TimeoutSeconds:  10,
		Config:          models.MonitorConfig{URL: "http://127.0.0.1:9/zz-lighthouse", Method: "GET"},
	}
	if err := monitors.Create(ctx, monitor, nil, nil); err != nil {
		t.Fatalf("creating the monitor: %v", err)
	}

	// One run per target, exactly as the job records them once TargetsFor/
	// BuildPlan handed it the two runs of a "both" monitor.
	ranAt := time.Now().UTC().Truncate(time.Second)
	mobileScore, desktopScore := 62, 94
	for strategy, score := range map[string]int{
		models.LighthouseStrategyMobile:  mobileScore,
		models.LighthouseStrategyDesktop: desktopScore,
	} {
		value := score
		err := lighthouse.Record(ctx, monitor.ID, &models.LighthouseResult{
			Status:                 models.LighthouseStatusOK,
			Strategy:               strategy,
			RanAt:                  ranAt,
			RanByNode:              cfg.NodeID,
			LighthouseMeasurements: models.LighthouseMeasurements{Performance: &value},
		})
		if err != nil {
			t.Fatalf("recording the %s run: %v", strategy, err)
		}
	}

	current, err := lighthouse.Get(ctx, monitor.ID)
	if err != nil {
		t.Fatalf("reading the current observations: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("the monitor holds %v, want one current row per run", strategiesOf(current))
	}
	for strategy, want := range map[string]int{
		models.LighthouseStrategyMobile:  mobileScore,
		models.LighthouseStrategyDesktop: desktopScore,
	} {
		row := current[strategy]
		if row == nil {
			t.Fatalf("the %s row is missing: %v", strategy, strategiesOf(current))
		}
		if row.Performance == nil || *row.Performance != want {
			t.Fatalf("the %s score = %v, want %d", strategy, row.Performance, want)
		}
	}

	history, err := lighthouse.History(ctx, monitor.ID, 10)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("the history holds %d runs, want 2: %+v", len(history), history)
	}
	seen := map[string]bool{}
	for _, run := range history {
		if run.Strategy == "" {
			t.Fatal("a history run was stored without a strategy: the chart cannot tell the runs apart")
		}
		seen[run.Strategy] = true
	}
	if !seen[models.LighthouseStrategyMobile] || !seen[models.LighthouseStrategyDesktop] {
		t.Fatalf("the history holds %v, want both runs", seen)
	}

	// A result that arrives without a strategy (an older node during a rolling
	// upgrade) is filed with the default run: the row stays one the UI can label.
	if err := lighthouse.Record(ctx, monitor.ID, &models.LighthouseResult{
		Status:                 models.LighthouseStatusOK,
		RanAt:                  ranAt.Add(time.Minute),
		LighthouseMeasurements: models.LighthouseMeasurements{Performance: &mobileScore},
	}); err != nil {
		t.Fatalf("recording an unlabelled run: %v", err)
	}
	current, err = lighthouse.Get(ctx, monitor.ID)
	if err != nil {
		t.Fatalf("reading the current observations: %v", err)
	}
	if current[models.DefaultLighthouseStrategy] == nil {
		t.Fatalf("an unlabelled run must be filed under %q: %v",
			models.DefaultLighthouseStrategy, strategiesOf(current))
	}
	if len(current) != 2 {
		t.Fatalf("the unlabelled run created a third row: %v", strategiesOf(current))
	}

	// Switching the monitor back to a single run drops the other one, which is
	// what ClearExcept does with the runs the monitor still measures.
	if err := lighthouse.ClearExcept(ctx, monitor.ID, []string{models.LighthouseStrategyDesktop}); err != nil {
		t.Fatalf("forgetting the runs that are no longer measured: %v", err)
	}
	current, err = lighthouse.Get(ctx, monitor.ID)
	if err != nil {
		t.Fatalf("reading the current observations: %v", err)
	}
	if len(current) != 1 || current[models.LighthouseStrategyDesktop] == nil {
		t.Fatalf("after the switch the monitor holds %v, want only the desktop run", strategiesOf(current))
	}
}

// strategiesOf lists the strategies a set of current rows holds, for the failure
// messages above (a map would print in a different order on every run).
func strategiesOf(rows map[string]*models.MonitorLighthouse) []string {
	out := make([]string, 0, len(rows))
	for strategy := range rows {
		out = append(out, strategy)
	}
	sort.Strings(out)
	return out
}
