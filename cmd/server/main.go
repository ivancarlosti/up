// Command server is the Up backend: it loads the environment, connects to the
// external MariaDB/MySQL database, runs the migrations, starts the scheduler
// and serves the REST API plus the embedded Vue frontend.
//
//	go run ./cmd/server        # local development (reads .env)
//	up                         # inside the container
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/database"
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

	// --- database (always external) ---------------------------------------
	db, err := database.Connect(cfg, log)
	if err != nil {
		return err
	}
	if err := database.Migrate(db, log); err != nil {
		return err
	}
	if err := database.Seed(ctx, db, cfg, log); err != nil {
		return err
	}

	// --- application ------------------------------------------------------
	app, err := newApplication(ctx, cfg, log, db)
	if err != nil {
		return err
	}
	defer app.stop()

	return app.serve(ctx)
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
