package services

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/checkers"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/expiry"
	"github.com/ivancarlosti/up/internal/models"
)

// ExpiryService is the daily job that refreshes the expiration of the two things
// Up watches beyond the probe itself: the TLS certificate of an endpoint and the
// registry registration of a domain.
//
// It answers the two challenges of the feature at once:
//
//   - deduplication: the worklist is built from the normalized targets, so 30
//     monitors on "https://www.example.com" and 12 on "app.example.com" produce
//     exactly one handshake and one registry lookup per run;
//   - cadence: the job runs once a day at the configured time, instead of
//     rewriting the stored certificate on every probe (the "1x por dia" rule the
//     certificate and domain alerts already follow).
type ExpiryService struct {
	cfg          *config.Config
	log          *slog.Logger
	settings     *SettingService
	monitors     *MonitorService
	certificates *CertificateService
	domains      *DomainService
	parsers      *WhoisParserService
	cluster      *ClusterService
	resolver     *expiry.Resolver
	// logMu guards loggedServedDay: the "today is already served" line is written
	// once per day instead of once per ticker tick (1440 times a day).
	logMu           sync.Mutex
	loggedServedDay int64
}

// NewExpiryService builds the expiry job.
func NewExpiryService(cfg *config.Config, log *slog.Logger, settings *SettingService,
	monitors *MonitorService, certificates *CertificateService, domains *DomainService,
	parsers *WhoisParserService, cluster *ClusterService) *ExpiryService {
	return &ExpiryService{
		cfg:          cfg,
		log:          log,
		settings:     settings,
		monitors:     monitors,
		certificates: certificates,
		domains:      domains,
		parsers:      parsers,
		cluster:      cluster,
		resolver:     domains.Resolver(),
	}
}

// Settings returns the current expiry configuration (used by the admin page).
func (s *ExpiryService) Settings() models.ExpirySettings { return s.settings.ExpirySettings() }

// Evaluate ages the stored certificates and domains without opening a socket and
// sends the reminders that are due. It runs at boot, so a date stored while the
// process was down (or a manual date just entered) is evaluated immediately.
func (s *ExpiryService) Evaluate(ctx context.Context) error {
	if s.certificates != nil {
		if err := s.certificates.Refresh(ctx); err != nil {
			return err
		}
	}
	if s.domains != nil {
		if err := s.domains.Refresh(ctx); err != nil {
			return err
		}
	}
	return nil
}

// RunDue runs the daily job when the configured time has passed and the day has
// not been served yet. It is called from the maintenance ticker.
//
// The day bucket is kept in the settings table so a restart does not repeat the
// work, and the cluster lock elects a single node: without it every node of a
// cluster would hit the registries once a day.
func (s *ExpiryService) RunDue(ctx context.Context, now time.Time) error {
	settings := s.settings.ExpirySettings()
	if now.Before(models.RunInstant(now, settings.CheckTime, settings.CheckTimezone)) {
		return nil // today's instant has not been reached yet
	}
	day := now.UTC().Unix() / 86400
	if s.settings.ExpiryLastRunDay() >= day {
		s.logServedOnce(day, now, settings)
		return nil
	}
	if s.cluster != nil {
		allowed, err := s.cluster.ClaimDailyJob(ctx, "expiry_run", day)
		if err != nil {
			return err
		}
		if !allowed {
			s.log.Debug("expiry job skipped: another node owns today's run")
			return nil
		}
	}
	s.log.Info("expiry job started",
		"day", day, "check_time", settings.CheckTime, "timezone", settings.CheckTimezone)
	if err := s.RunNow(ctx); err != nil {
		return err
	}
	if err := s.settings.SetExpiryLastRunDay(ctx, day); err != nil {
		s.log.Warn("could not record the expiry run day", "error", err)
	}
	return nil
}

// logServedOnce reports that today's run is already done, at most once per day.
//
// The job ticks every minute and is silent for the remaining 1439 of them:
// without this line an operator reading the container logs cannot tell a
// scheduled job from one that never runs.
func (s *ExpiryService) logServedOnce(day int64, now time.Time, settings models.ExpirySettings) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.loggedServedDay == day {
		return
	}
	s.loggedServedDay = day
	s.log.Info("expiry job: today is already served",
		"check_time", settings.CheckTime, "timezone", settings.CheckTimezone,
		"next_run", models.NextDailyRun(now, settings.CheckTime, settings.CheckTimezone).Format(time.RFC3339))
}

// expiryRun is one prepared worklist: the resolver configured with the current
// settings and parsers, plus the deduplicated targets of the active watchers.
type expiryRun struct {
	plan     expiryPlan
	settings models.ExpirySettings
}

// prepare loads the configuration and the watchers and builds the worklist. It is
// the single definition of "what a run looks up", shared by the daily job and by
// the per-target refresh of the admin view.
func (s *ExpiryService) prepare(ctx context.Context) (*expiryRun, error) {
	settings := s.settings.ExpirySettings()
	parsers := []models.WhoisParser{}
	if s.parsers != nil {
		loaded, err := s.parsers.Enabled(ctx)
		if err != nil {
			return nil, err
		}
		parsers = loaded
	}
	s.resolver.Configure(settings, parsers)

	watchers, err := s.monitors.ExpiryWatchers(ctx)
	if err != nil {
		return nil, err
	}
	return &expiryRun{plan: planExpiryTargets(watchers), settings: settings}, nil
}

// RunNow refreshes every deduplicated target and evaluates the alerts. It is the
// manual "run now" action and the body of the daily job.
func (s *ExpiryService) RunNow(ctx context.Context) error {
	run, err := s.prepare(ctx)
	if err != nil {
		return err
	}
	if len(run.plan.certificates) == 0 && len(run.plan.domains) == 0 && len(run.plan.manual) == 0 {
		s.log.Debug("expiry job: nothing to watch")
		return nil
	}

	now := time.Now().UTC()
	timeout := time.Duration(run.settings.TimeoutSeconds) * time.Second

	certificates := 0
	for key, target := range run.plan.certificates {
		certificates += s.refreshCertificate(ctx, target, run.plan.certWatchers[key], now, timeout)
	}

	domains := 0
	for domain, monitors := range run.plan.domains {
		domains += s.refreshDomain(ctx, domain, monitors, false, now)
	}
	for domain, monitors := range run.plan.manual {
		domains += s.refreshDomain(ctx, domain, monitors, true, now)
	}

	s.log.Info("expiry job finished",
		"certificate_targets", len(run.plan.certificates), "certificate_monitors", certificates,
		"domain_targets", len(run.plan.domains), "domain_monitors", domains,
		"rate_limit_ms", run.settings.RateLimitMS)
	return nil
}

// refreshCertificate performs the single handshake of a target and fans the
// observation out to every monitor that shares it. It returns how many monitors
// were updated.
func (s *ExpiryService) refreshCertificate(ctx context.Context, target expiry.CertificateTarget, monitors []*models.Monitor, now time.Time, timeout time.Duration) int {
	if len(monitors) == 0 {
		return 0
	}
	info, probeErr := checkers.ProbeCertificate(ctx, target.Address(), target.ServerName, target.Insecure, timeout)
	if probeErr != nil && info == nil {
		s.log.Warn("expiry job: certificate lookup failed", "target", target.Address(), "error", probeErr)
		return 0
	}
	info.CapturedByNode = s.cfg.NodeID
	for _, monitor := range monitors {
		s.recordCertificate(ctx, monitor, info, now)
	}
	return len(monitors)
}

// refreshDomain performs the single registry lookup of a target (a manual date
// never touches the network) and fans the observation out to every monitor that
// shares it. It returns how many monitors were updated.
func (s *ExpiryService) refreshDomain(ctx context.Context, domain string, monitors []*models.Monitor, manual bool, now time.Time) int {
	if len(monitors) == 0 {
		return 0
	}
	var manualDate *time.Time
	if manual && monitors[0].DomainExpiresAt != nil {
		manualDate = monitors[0].DomainExpiresAt
	}
	info := s.resolver.Resolve(ctx, domain, manualDate)
	info.CheckedByNode = s.cfg.NodeID
	for _, monitor := range monitors {
		s.recordDomain(ctx, monitor, info, now)
	}
	return len(monitors)
}

// recordCertificate stores the observation of one monitor and evaluates its
// certificate alerts.
func (s *ExpiryService) recordCertificate(ctx context.Context, monitor *models.Monitor, info *models.CertificateInfo, now time.Time) {
	if _, err := s.certificates.Record(ctx, monitor.ID, s.cfg.NodeID, info); err != nil {
		s.log.Error("expiry job: could not store the certificate", "monitor_id", monitor.ID, "error", err)
		return
	}
	row, err := s.certificates.Get(ctx, monitor.ID)
	if err != nil || row == nil {
		return
	}
	if event, ok := s.certificates.Evaluate(ctx, row, monitor, now); ok {
		s.certificates.publish("monitor.certificate", map[string]any{
			"monitor_id": monitor.ID,
			"event":      event,
			"days_left":  row.DaysLeft,
			"not_after":  row.NotAfter,
		})
	}
}

// recordDomain stores the observation of one monitor and evaluates its domain
// alerts.
func (s *ExpiryService) recordDomain(ctx context.Context, monitor *models.Monitor, info *models.DomainInfo, now time.Time) {
	if _, err := s.domains.Record(ctx, monitor.ID, s.cfg.NodeID, info); err != nil {
		s.log.Error("expiry job: could not store the domain", "monitor_id", monitor.ID, "error", err)
		return
	}
	row, err := s.domains.Get(ctx, monitor.ID)
	if err != nil || row == nil {
		return
	}
	if event, ok := s.domains.Evaluate(ctx, row, monitor, now); ok {
		s.domains.publish("monitor.domain", map[string]any{
			"monitor_id": monitor.ID,
			"event":      event,
			"domain":     row.Domain,
			"days_left":  row.DaysLeft,
			"expires_at": row.ExpiresAt,
		})
	}
}
