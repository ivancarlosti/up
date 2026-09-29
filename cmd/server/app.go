package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/boot"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/handlers"
	"github.com/ivancarlosti/up/internal/notify"
	"github.com/ivancarlosti/up/internal/scheduler"
	"github.com/ivancarlosti/up/internal/services"
	"github.com/ivancarlosti/up/internal/version"
	"github.com/ivancarlosti/up/internal/ws"
)

// application holds the wired dependencies and the background workers.
//
// The HTTP server is deliberately NOT here: it is created and started before the
// application exists, so the SPA is served while the database is being prepared
// (see cmd/server/main.go and internal/handlers.NewBootstrapEngine).
type application struct {
	cfg       *config.Config
	log       *slog.Logger
	engine    *gin.Engine
	scheduler *scheduler.Scheduler
	hubDone   chan struct{}
}

// newApplication builds every service, wires them together and starts the
// background workers. It returns the engine serving the real API, which the
// caller installs on the already listening server.
func newApplication(ctx context.Context, cfg *config.Config, log *slog.Logger, db *gorm.DB, state *boot.State) (*application, error) {
	// --- services ---------------------------------------------------------
	settings := services.NewSettingService(db, cfg, log)
	stats := services.NewStatsService(db)
	monitors := services.NewMonitorService(db, cfg, log, stats)
	monitorGroups := services.NewMonitorGroupService(db, cfg, log)
	monitorTemplates := services.NewMonitorTemplateService(db, cfg, log)
	certificates := services.NewCertificateService(db, cfg, log)
	domains := services.NewDomainService(db, cfg, log)
	whoisParsers := services.NewWhoisParserService(db, cfg, log)
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
	monitors.SetSettingService(settings)
	monitorGroups.SetPublisher(hub)
	monitorGroups.SetMonitorService(monitors)
	monitorTemplates.SetPublisher(hub)
	monitorTemplates.SetMonitorService(monitors)
	certificates.SetPublisher(hub)
	certificates.SetMonitorService(monitors)
	certificates.SetNotificationService(notifications)
	certificates.SetClusterService(cluster)
	domains.SetPublisher(hub)
	domains.SetMonitorService(monitors)
	domains.SetNotificationService(notifications)
	domains.SetClusterService(cluster)
	// The monitor write path applies a manual date to the stored observation of
	// the whole domain as soon as it is saved, so the badge appears without
	// waiting for the daily job.
	monitors.SetDomainService(domains)
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
	monitorGroups.SetSyncEmitter(syncEmitter)
	monitorTemplates.SetSyncEmitter(syncEmitter)
	statusPages.SetSyncEmitter(syncEmitter)
	notifications.SetSyncEmitter(syncEmitter)
	syncService := services.NewSyncService(db, cfg, log, cluster, syncEmitter)
	syncService.SetPublisher(hub)
	// The opt-in settings sync needs the local store, so an applied setting refreshes the
	// cache the readers use instead of only the row.
	syncService.SetSettingsService(settings)

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
	// --- daily expiry job -------------------------------------------------
	// Certificate and domain expiration are refreshed once a day (at the time
	// configured in Admin > TLD/SSL expiration) over the deduplicated targets,
	// instead of being rewritten by every probe.
	expiries := services.NewExpiryService(cfg, log, settings, monitors, certificates, domains, whoisParsers, cluster)
	container := &handlers.Container{
		Cfg:              cfg,
		Log:              log,
		DB:               db,
		State:            state,
		Settings:         settings,
		Monitors:         monitors,
		MonitorGroups:    monitorGroups,
		MonitorTemplates: monitorTemplates,
		Certificates:     certificates,
		Domains:          domains,
		WhoisParsers:     whoisParsers,
		Expiry:           expiries,
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
	sched.SetExpiryService(expiries)
	sched.SetNotificationService(notifications)
	sched.SetSyncService(syncService)
	sched.SetSettingService(settings)
	state.SetPhase(boot.PhaseScheduler)
	if err := sched.Start(ctx); err != nil {
		return nil, err
	}
	sched.StartMaintenance(ctx)

	return &application{
		cfg:       cfg,
		log:       log,
		engine:    engine,
		scheduler: sched,
		hubDone:   hubDone,
	}, nil
}

// startHTTP starts the listener in the background and returns the channel that
// reports a listener that could not open (a busy port, a missing privilege).
//
// It returns immediately on purpose: the caller is expected to keep preparing
// the database while the process already answers. The first response comes from
// the bootstrap engine, which tells the browser what is going on.
func startHTTP(server *http.Server, cfg *config.Config, log *slog.Logger) <-chan error {
	serverErrors := make(chan error, 1)
	go func() {
		log.Info("http server listening",
			"addr", server.Addr,
			"app_url", cfg.AppURL,
			"auth", string(cfg.AuthMethod),
			"cluster", cfg.ClusterEnabled,
			"version", version.Readable(),
			"database", "connecting",
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	return serverErrors
}

// shutdownHTTP drains the connections (including the WebSocket ones).
func shutdownHTTP(server *http.Server, log *slog.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown did not finish cleanly", "error", err)
	}
}

// handlerSwitcher is the http.Handler the listener is built with: it holds the
// engine that answers right now and lets the boot replace it.
//
// The process accepts connections before the API exists, so the listener cannot
// be created with the real engine. A pointer swap is what makes the change
// invisible to the clients that are already connected (the SPA polling
// /api/boot, a status page, a WebSocket handshake waiting for the hub): the
// request that arrives after the swap is served by the wired engine, and no
// connection is ever refused in between.
type handlerSwitcher struct {
	current atomic.Pointer[gin.Engine]
}

// newHandlerSwitcher installs the first engine (the bootstrap one).
func newHandlerSwitcher(engine *gin.Engine) *handlerSwitcher {
	switcher := &handlerSwitcher{}
	switcher.Set(engine)
	return switcher
}

// Set installs the engine that answers from now on.
func (s *handlerSwitcher) Set(engine *gin.Engine) {
	s.current.Store(engine)
}

// ServeHTTP dispatches to the installed engine.
func (s *handlerSwitcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if engine := s.current.Load(); engine != nil {
		engine.ServeHTTP(w, r)
		return
	}
	http.Error(w, "the server is starting", http.StatusServiceUnavailable)
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
