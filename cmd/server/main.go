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
	"strings"
	"syscall"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/boot"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/database"
	"github.com/ivancarlosti/up/internal/handlers"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/services"
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

// The boot steps after the connection are retried with a capped exponential
// backoff: fast enough that a momentary failure (a 502 from the identity
// provider, a lock timeout on a migration) costs seconds, slow enough that a
// cause needing a human - a wrong issuer, a revoked grant - is not hammered
// every second. The connection itself keeps its own, faster schedule (see
// database.Connect), because connecting is cheap and the steps above are not.
//
// Every step is idempotent by construction (see the doc of each database.*
// function), which is what makes retrying them safe: a run that fails halfway
// leaves the schema and the data in a state the next run completes.
const (
	bootRetryInitial = 15 * time.Second
	bootRetryMax     = 5 * time.Minute
)

// nextBackoff doubles the wait, up to bootRetryMax, and stays there.
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > bootRetryMax {
		next = bootRetryMax
	}
	return next
}

func run() error {
	// --- configuration ----------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	log.Info("starting Up", "config", cfg.Summary())
	warnDeprecatedRecaptchaEnv(log)

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
	// The retry signal is shared by every wait loop of the boot - the connection
	// and the steps below. It is what the "try now" button of the boot screen
	// pulls (see handlers.bootRetry).
	retry := boot.NewRetry()
	switcher := newHandlerSwitcher(handlers.NewBootstrapEngine(cfg, log, state, retry))
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
	db, err := database.Connect(ctx, cfg, log, state, retry)
	if err != nil {
		if ctx.Err() != nil {
			// Stopped while waiting for the database: not a failure.
			return nil
		}
		return err
	}

	// --- schema, data and application -------------------------------------
	// Every step publishes the phase it is in, so a browser that is already
	// open says what the process is doing, and every duration lands in the
	// "boot completed" line below (the numbers this order was chosen from).
	//
	// Both stages run inside a retry loop, for as long as the process runs: a
	// step that fails on a transient cause (a lock timeout, a replica that is
	// briefly read only, an issuer that answers 502) recovers on its own instead
	// of parking the process in an error only a restart could clear. It stays up
	// and keeps answering /api/boot with the classified failure while it waits,
	// and "try now" on the boot screen skips the rest of the backoff.
	timings := []any{}
	var (
		app      *application
		deferred *database.RollupPlan
		attempt  int
		backoff  = bootRetryInitial
		prepped  bool
	)
	for {
		// Stage one: the schema and the data. Skipped once it has succeeded, so
		// a wiring failure does not re-run the migrations every attempt.
		if !prepped {
			// The numbers of a failed attempt are not what the "boot completed"
			// line is for: keep only the attempt that worked.
			timings = timings[:0]
			deferred, err = prepareDatabase(ctx, cfg, log, db, state, &timings)
			if err == nil {
				// Progress: the counters describe the stage that is failing, so
				// they restart from a clean slate.
				prepped, attempt, backoff = true, 0, bootRetryInitial
			}
		}

		// Stage two: the application, on a schema that is ready.
		if prepped {
			state.SetPhase(boot.PhaseWiring)
			began := time.Now()
			app, err = newApplication(ctx, cfg, log, db, state)
			if err == nil {
				timings = append(timings, "wiring_ms", time.Since(began).Milliseconds())
				break
			}
			err = fail(i18n.CodeBootFailed, err, "wiring the services")
		}

		// A stage failed: report it, then wait before trying again.
		attempt++
		code := recordRetry(state, attempt, err, backoff)
		log.Warn("a boot step failed, retrying",
			"attempt", attempt, "in", backoff.String(), "code", code, "error", err)

		select {
		case <-ctx.Done():
			// The process was asked to stop. The failure is already on
			// /api/boot for whoever looks at it; retrying would only delay the
			// drain.
			return nil
		case <-retry.Chan():
			log.Info("boot retry requested by the web interface", "attempt", attempt)
		case serverErr := <-serverErrors:
			return fmt.Errorf("http server failed: %w", serverErr)
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff)
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
//
// Every step it runs is idempotent by construction, which is what lets the
// caller retry the whole sequence when one of them fails (see run): a run that
// stops halfway is completed by the next one, and a step that has nothing left
// to do is a no-op. It records the phase of each step as it goes, so the
// browser watching /api/boot sees the retry make progress rather than a frozen
// failure.
func prepareDatabase(ctx context.Context, cfg *config.Config, log *slog.Logger, db *gorm.DB, state *boot.State, timings *[]any) (*database.RollupPlan, error) {
	step := func(name string, phase boot.Phase, fallback string, run func() error) error {
		state.SetPhase(phase)
		began := time.Now()
		err := run()
		*timings = append(*timings, name+"_ms", time.Since(began).Milliseconds())
		if err == nil {
			return nil
		}
		return fail(fallback, err, name)
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

	// The monitors that predate the single group rule are reduced to one group
	// each. It runs AFTER the backfill on purpose: the tombstones it publishes name
	// the uuid of both ends, and a row that predates the sync identity has no uuid
	// until the backfill has stamped it. The phase is the backfill one: this IS a
	// data backfill (it rewrites rows an older release wrote), and reusing the phase
	// keeps the progress the UI reports honest without adding a sentence to every
	// locale. It is idempotent, so a second boot changes nothing.
	if err := step("monitor_groups", boot.PhaseBackfilling, i18n.CodeDatabaseMigration, func() error {
		return services.ConsolidateSingleGroupMembership(ctx, db, cfg, log)
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
		return nil, fail(i18n.CodeDatabaseMigration, err, "planning the heartbeat rollups")
	}
	deferred := plan.Full
	if deferred {
		state.SetMaintenance(boot.MaintenanceRollups)
		log.Info("heartbeat rollup rebuild deferred until the API is serving",
			"from", plan.Start, "to", plan.End)
	} else if err := database.RebuildRollups(ctx, db, log, plan); err != nil {
		return nil, fail(i18n.CodeDatabaseMigration, err, "rebuilding the heartbeat rollups")
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

// bootFailure is a boot step error carrying the stable code and the developer
// detail the web interface reads.
//
// A step that knows its own fallback code (a migration, a backfill) classifies
// its error through fail, and the boot loop - which records the failure now
// that a step is retried instead of parked - reads the code back with
// errors.As.
type bootFailure struct {
	code   string
	detail string
	err    error
}

// Error is the wrapped step error ("migrate: running automatic migrations: ...").
func (e *bootFailure) Error() string { return e.err.Error() }

// Unwrap exposes the original error to errors.Is and errors.As.
func (e *bootFailure) Unwrap() error { return e.err }

// fail classifies a boot step error and returns it wrapped with the step that
// produced it.
//
// It deliberately records nothing: the boot loop owns the state and the retry
// counters (see recordRetry), so a step that fails can be attempted again
// without the state ever claiming a final failure. That is what keeps the
// "try now" button meaningful - a frozen ERR_BOOT_FAILED was the whole problem.
func fail(fallback string, err error, step string) error {
	code, detail := boot.Classify(err)
	if code == "" {
		code = fallback
	}
	return &bootFailure{
		code:   code,
		detail: detail,
		err:    fmt.Errorf("%s: %w", step, err),
	}
}

// recordRetry publishes a failed attempt and the wait that follows it, and
// returns the code the operator will read.
//
// Publishing the failure instead of waiting for it to be final is what the boot
// screen needs: the SPA explains it in the language of the visitor (see
// internal/handlers and web/src/views/BootView.vue) while the loop keeps
// waiting, and /api/health stays unhealthy so a container health check does not
// declare a process healthy while it cannot serve.
func recordRetry(state *boot.State, attempt int, err error, next time.Duration) string {
	var failure *bootFailure
	if errors.As(err, &failure) {
		state.Retry(attempt, failure.code, failure.detail, next)
		return failure.code
	}
	// An error raised outside a step: classify it here so the state never
	// degrades without a code.
	code, detail := boot.Classify(err)
	if code == "" {
		code = i18n.CodeBootFailed
	}
	state.Retry(attempt, code, detail, next)
	return code
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

// warnDeprecatedRecaptchaEnv tells an operator whose .env still carries the
// reCAPTCHA variables that they are not read any more. Nothing consumes them:
// the keys live in the settings table since they moved to Admin > Settings, so
// a restart would otherwise drop the captcha without a word.
func warnDeprecatedRecaptchaEnv(log *slog.Logger) {
	for _, key := range []string{"RECAPTCHA_CLIENTID", "RECAPTCHA_CLIENTSECRET"} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			continue
		}
		log.Warn("the reCAPTCHA keys are configured in Admin > Settings now: "+
			key+" is ignored, paste the value once in the settings page", "variable", key)
	}
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
