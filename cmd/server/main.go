// Command server is the Up backend: it serves the embedded Vue frontend
// immediately, prepares the external MariaDB/MySQL database (connection,
// migrations, backfills, seed) while reporting the phase it is in, then swaps
// the API in and starts the scheduler.
//
//	go run ./cmd/server        # local development (reads .env)
//	up                         # inside the container
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/boot"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/database"
	"github.com/ivancarlosti/up/internal/handlers"
	"github.com/ivancarlosti/up/internal/i18n"
)

func main() {
	if err := run(); err != nil {
		var validation *config.ValidationError
		if errors.As(err, &validation) {
			fmt.Fprintln(os.Stderr, "Up cannot start: invalid configuration")
			fmt.Fprintln(os.Stderr, validation.Error())
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "Up cannot start:", err)
		os.Exit(1)
	}
}

func run() error {
	// --- configuration ----------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	log.Info("starting Up", "config", cfg.Summary())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()

	// --- the listener opens before the database ----------------------------
	// The SPA is served from the first millisecond, before the database is even
	// contacted. Two reasons: a start that takes 30 seconds must not look like a
	// dead application (the port used to stay closed until every migration and
	// backfill had finished), and a database that never answers must not look
	// like one either (the process used to exit after 90 seconds and restart in
	// a loop that hid the reason). The bootstrap engine answers with the phase
	// below until the wired engine replaces it.
	state := boot.New(cfg.NodeID)
	switcher := newHandlerSwitcher(handlers.NewBootstrapEngine(cfg, log, state))
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.AppPort),
		Handler:           switcher,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // WebSocket connections must stay open
		IdleTimeout:       120 * time.Second,
	}
	serverErrors := startHTTP(server, cfg, log)
	defer shutdownHTTP(server, log)

	// --- database (always external) ---------------------------------------
	state.SetPhase(boot.PhaseConnecting)
	db, err := database.Connect(ctx, cfg, log, state)
	if err != nil {
		if ctx.Err() != nil {
			// Stopped while waiting for the database: not a failure.
			return nil
		}
		return err
	}

	// --- schema and data --------------------------------------------------
	// Every step publishes the phase it is in, so a browser that is already
	// open says what the process is doing, and every duration lands in the
	// "boot completed" line below (the numbers this order was chosen from).
	timings := []any{}
	deferred, err := prepareDatabase(ctx, cfg, log, db, state, &timings)
	if err != nil {
		return serveFailure(ctx, log, state, serverErrors, err)
	}

	// --- application ------------------------------------------------------
	state.SetPhase(boot.PhaseWiring)
	began := time.Now()
	app, err := newApplication(ctx, cfg, log, db, state)
	timings = append(timings, "wiring_ms", time.Since(began).Milliseconds())
	if err != nil {
		return serveFailure(ctx, log, state, serverErrors,
			fail(state, i18n.CodeBootFailed, err, "wiring the services"))
	}
	defer app.stop()

	// The swap is what opens the API: from here on the same listener serves the
	// wired engine, without closing a single connection.
	switcher.Set(app.engine)
	state.Ready()
	timings = append(timings, "total_ms", time.Since(started).Milliseconds())
	log.Info("boot completed", timings...)

	if deferred != nil {
		startDeferredRollups(ctx, db, log, state, *deferred)
	}

	return waitForShutdown(ctx, log, serverErrors)
}

// prepareDatabase runs every boot step that touches the database, in order.
//
// It returns the rollup plan when the full rebuild was deferred to the
// background (nil when there was nothing left to do), so the caller knows that
// the statistics still complete a few seconds after the API starts answering.
func prepareDatabase(ctx context.Context, cfg *config.Config, log *slog.Logger, db *gorm.DB, state *boot.State, timings *[]any) (*database.RollupPlan, error) {
	step := func(name string, phase boot.Phase, fallback string, run func() error) error {
		state.SetPhase(phase)
		began := time.Now()
		err := run()
		*timings = append(*timings, name+"_ms", time.Since(began).Milliseconds())
		if err == nil {
			return nil
		}
		return fail(state, fallback, err, name)
	}

	if err := step("migrate", boot.PhaseMigrating, i18n.CodeDatabaseMigration, func() error {
		return database.Migrate(db, log)
	}); err != nil {
		return nil, err
	}

	// The sync identity (uuid / origin_node_id / revision) is stamped on the
	// rows that predate it, right after the columns exist. Idempotent: the
	// second boot changes nothing.
	if err := step("backfill", boot.PhaseBackfilling, i18n.CodeDatabaseMigration, func() error {
		return database.Backfill(ctx, db, cfg, log)
	}); err != nil {
		return nil, err
	}

	// Authentication is a property of the monitor, never of a template: the
	// credentials a template may still store are removed here (see the doc of
	// database.BackfillTemplateAuth). Idempotent as well.
	if err := step("template_auth", boot.PhaseTemplateAuth, i18n.CodeDatabaseMigration, func() error {
		return database.BackfillTemplateAuth(ctx, db, log)
	}); err != nil {
		return nil, err
	}

	// The hourly rollups of the window statistics are reconciled with the raw
	// heartbeats. The plan says how much is left to do, and the first boot after
	// the upgrade has to reduce 30 days of raw heartbeats: that pass is deferred
	// while the API starts serving (startDeferredRollups), which needs no lock
	// because the plan never touches the current hour and the live probes only
	// ever write the current hour.
	state.SetPhase(boot.PhaseRollups)
	began := time.Now()
	plan, err := database.PlanRollups(ctx, db)
	if err != nil {
		return nil, fail(state, i18n.CodeDatabaseMigration, err, "planning the heartbeat rollups")
	}
	deferred := plan.Full
	if deferred {
		state.SetMaintenance(boot.MaintenanceRollups)
		log.Info("heartbeat rollup rebuild deferred until the API is serving",
			"from", plan.Start, "to", plan.End)
	} else if err := database.RebuildRollups(ctx, db, log, plan); err != nil {
		return nil, fail(state, i18n.CodeDatabaseMigration, err, "rebuilding the heartbeat rollups")
	}
	*timings = append(*timings, "rollups_ms", time.Since(began).Milliseconds())

	if err := step("seed", boot.PhaseSeeding, i18n.CodeBootFailed, func() error {
		return database.Seed(ctx, db, cfg, log)
	}); err != nil {
		return nil, err
	}

	if deferred {
		return &plan, nil
	}
	return nil, nil
}


// startDeferredRollups runs the full rollup rebuild after the API started
// serving.
//
// The range is [start, floor) and the live probes only ever write the buckets
// from the floor on, so the pass needs no lock and cannot overwrite a fresh
// bucket. It is moved here only because reducing 30 days of raw heartbeats is
// the one boot step long enough to delay every user.
func startDeferredRollups(ctx context.Context, db *gorm.DB, log *slog.Logger, state *boot.State, plan database.RollupPlan) {
	go func() {
		began := time.Now()
		defer state.ClearMaintenance()
		if err := database.RebuildRollups(ctx, db, log, plan); err != nil {
			state.Fail(i18n.CodeDatabaseMigration, err.Error())
			log.Error("the deferred heartbeat rollup rebuild failed: the window statistics stay incomplete until the next boot",
				"error", err, "from", plan.Start, "to", plan.End)
			return
		}
		log.Info("deferred heartbeat rollup rebuild finished", "took_ms", time.Since(began).Milliseconds())
	}()
}

// fail records a boot failure with its classified code and returns it wrapped
// with the step that produced it.
func fail(state *boot.State, fallback string, err error, step string) error {
	code, detail := boot.Classify(err)
	if code == "" {
		code = fallback
	}
	state.Fail(code, detail)
	return fmt.Errorf("%s: %w", step, err)
}

// serveFailure keeps the process up when a boot step failed.
//
// The bootstrap engine is still installed, so the SPA explains the failure in
// the language of the visitor and the health check stays unhealthy. Exiting
// would only restart the same failure and hide the reason behind a container
// restart counter, which is exactly what this design set out to fix.
func serveFailure(ctx context.Context, log *slog.Logger, state *boot.State, serverErrors <-chan error, err error) error {
	snap := state.Snapshot()
	log.Error("Up could not finish starting: the failure is reported by the web interface and by /api/health",
		"code", snap.Code, "database", snap.Database, "phase", snap.Phase, "error", err)
	return waitForShutdown(ctx, log, serverErrors)
}

// waitForShutdown blocks until the process is asked to stop or the listener
// dies. The drain itself is the deferred shutdownHTTP of run().
func waitForShutdown(ctx context.Context, log *slog.Logger, serverErrors <-chan error) error {
	select {
	case err := <-serverErrors:
		return fmt.Errorf("http server failed: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")
	}
	return nil
}

// newLogger builds the structured logger used by the whole application.
func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel})
	return slog.New(handler)
}
