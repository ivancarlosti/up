package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/handlers"
	"github.com/ivancarlosti/up/internal/notify"
	"github.com/ivancarlosti/up/internal/scheduler"
	"github.com/ivancarlosti/up/internal/services"
	"github.com/ivancarlosti/up/internal/version"
	"github.com/ivancarlosti/up/internal/ws"
)

// application holds the wired dependencies and the HTTP server.
type application struct {
	cfg       *config.Config
	log       *slog.Logger
	server    *http.Server
	scheduler *scheduler.Scheduler
	hubDone   chan struct{}
}

// newApplication builds every service, wires them together and prepares the
// HTTP server.
func newApplication(ctx context.Context, cfg *config.Config, log *slog.Logger, db *gorm.DB) (*application, error) {
	// --- services ---------------------------------------------------------
	settings := services.NewSettingService(db, cfg, log)
	stats := services.NewStatsService(db)
	monitors := services.NewMonitorService(db, cfg, log, stats)
	monitorGroups := services.NewMonitorGroupService(db, cfg, log)
	monitorTemplates := services.NewMonitorTemplateService(db, cfg, log)
	certificates := services.NewCertificateService(db, cfg, log)
	heartbeats := services.NewHeartbeatService(db, cfg, log)
	notificationEngine := notify.NewEngine(log, 15*time.Second)
	notifications := services.NewNotificationService(db, cfg, log, notificationEngine)
	cluster := services.NewClusterService(db, cfg, log, settings, stats)
	statusPages := services.NewStatusPageService(db, cfg, log)
	tokens := services.NewTokenService(db, log)
	ipRules := services.NewIPRuleService(db, cfg, log)
	sessions := services.NewSessionService(cfg, settings, log)

	// --- real time hub ----------------------------------------------------
	hub := ws.NewHub(log, []string{cfg.AppURL})
	hubDone := make(chan struct{})
	go hub.Run(hubDone)

	// --- wiring -----------------------------------------------------------
	monitors.SetPublisher(hub)
	monitors.SetVoteProvider(cluster)
	monitorGroups.SetPublisher(hub)
	monitorGroups.SetMonitorService(monitors)
	monitorTemplates.SetPublisher(hub)
	monitorTemplates.SetMonitorService(monitors)
	certificates.SetPublisher(hub)
	certificates.SetMonitorService(monitors)
	certificates.SetNotificationService(notifications)
	certificates.SetClusterService(cluster)
	heartbeats.SetPublisher(hub)
	notifications.SetPublisher(hub)
	cluster.SetPublisher(hub)
	cluster.SetMonitorService(monitors)
	cluster.SetNotificationService(notifications)
	statusPages.SetMonitorService(monitors)

	// --- federated synchronisation ----------------------------------------
	// The emitter publishes local writes; the sync service pulls the peers. In
	// any mode but federated the emitter is disabled and pulls do nothing, so
	// wiring them unconditionally keeps the container simple.
	syncEmitter := services.NewSyncEmitter(db, cfg, log)
	monitors.SetSyncEmitter(syncEmitter)
	syncService := services.NewSyncService(db, cfg, log, cluster, syncEmitter)
	syncService.SetPublisher(hub)

	if err := cluster.EnsureSelf(ctx); err != nil {
		return nil, err
	}

	// --- OIDC (only when AUTH_METHOD=keycloak) ---------------------------
	oidc, err := handlers.NewOIDCProvider(ctx, cfg, log)
	if err != nil {
		return nil, err
	}

	// --- HTTP -------------------------------------------------------------
	if cfg.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}
	engine := gin.New()
	container := &handlers.Container{
		Cfg:              cfg,
		Log:              log,
		DB:               db,
		Settings:         settings,
		Monitors:         monitors,
		MonitorGroups:    monitorGroups,
		MonitorTemplates: monitorTemplates,
		Heartbeats:       heartbeats,
		Stats:            stats,
		Notifications:    notifications,
		Cluster:          cluster,
		Sync:             syncService,
		StatusPages:      statusPages,
		Tokens:           tokens,
		IPRules:          ipRules,
		Sessions:         sessions,
		Hub:              hub,
		OIDC:             oidc,
		SPA:              handlers.NewSPAHandler(log),
	}
	container.Register(engine)

	// --- scheduler --------------------------------------------------------
	sched := scheduler.New(cfg, log, monitors, heartbeats, cluster, stats)
	// The container was built before the scheduler (the scheduler needs the
	// services), so it is attached here.
	container.Scheduler = sched
	sched.SetCertificateService(certificates)
	sched.SetNotificationService(notifications)
	sched.SetSyncService(syncService)
	if err := sched.Start(ctx); err != nil {
		return nil, err
	}
	sched.StartMaintenance(ctx)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.AppPort),
		Handler:           engine,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // WebSocket connections must stay open
		IdleTimeout:       120 * time.Second,
	}

	return &application{
		cfg:       cfg,
		log:       log,
		server:    server,
		scheduler: sched,
		hubDone:   hubDone,
	}, nil
}

// serve starts the HTTP server and blocks until the context is cancelled.
func (a *application) serve(ctx context.Context) error {
	serverErrors := make(chan error, 1)
	go func() {
		a.log.Info("http server listening",
			"addr", a.server.Addr,
			"app_url", a.cfg.AppURL,
			"auth", string(a.cfg.AuthMethod),
			"cluster", a.cfg.ClusterEnabled,
			"version", version.Readable(),
		)
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	select {
	case err := <-serverErrors:
		return fmt.Errorf("http server failed: %w", err)
	case <-ctx.Done():
		a.log.Info("shutdown signal received, draining connections")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.server.Shutdown(shutdownCtx); err != nil {
		a.log.Warn("graceful shutdown did not finish cleanly", "error", err)
	}
	return nil
}

// stop releases the background workers.
func (a *application) stop() {
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
	if a.hubDone != nil {
		close(a.hubDone)
	}
	a.log.Info("Up stopped")
}
