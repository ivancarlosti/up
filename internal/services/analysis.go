package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/checkers"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/expiry"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/lighthouse"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/notify"
)

// AnalysisService runs the on-demand probes of POST /api/v1/analyze.
//
// It is deliberately stateless: nothing is written to the database, no monitor
// is created, no heartbeat is stored, no notification can fire. The endpoint
// answers "what would a monitor of this target see right now?" and throws the
// observation away, which is what makes it safe to hand to a script or a
// pipeline.
//
// Every probe it runs is the one the scheduler and the daily jobs already use:
// checkers.Check for the uptime verdict, checkers.ProbeCertificate for the TLS
// chain, the shared expiry resolver for the registry date and
// LighthouseService.MeasureTarget for the page score. The service owns no
// probing logic of its own, only the fan out and the validation.
type AnalysisService struct {
	cfg        *config.Config
	log        *slog.Logger
	lighthouse *LighthouseService
	domains    *DomainService
}

// NewAnalysisService builds the on-demand analysis service.
//
// The two collaborators are the services that own outbound calls with settings
// (the web performance job and the domain resolver); a nil one turns the
// matching check into a section that says it is not wired, so a half built
// process answers instead of panicking.
func NewAnalysisService(cfg *config.Config, log *slog.Logger, lighthouse *LighthouseService, domains *DomainService) *AnalysisService {
	return &AnalysisService{cfg: cfg, log: log, lighthouse: lighthouse, domains: domains}
}

// The probes an analysis can run. They are the check names on the wire.
const (
	AnalysisCheckUptime      = "uptime"
	AnalysisCheckCertificate = "certificate"
	AnalysisCheckDomain      = "domain"
	AnalysisCheckPerformance = "performance"
)

// analysisChecks lists the check names in the order the documentation and the
// validation messages show them.
var analysisChecks = []string{
	AnalysisCheckUptime,
	AnalysisCheckCertificate,
	AnalysisCheckDomain,
	AnalysisCheckPerformance,
}

// Section statuses: the outcome of one probe. `ok` means the observation is
// there, `error` means it could not be read (and Error says why). The uptime
// section is the exception and reports the probe verdict itself (up, down,
// pending, maintenance), exactly like a heartbeat.
const (
	analysisStatusOK    = "ok"
	analysisStatusError = "error"
)

// Bounds of the request options. They are tighter than the monitor ones on
// purpose: the endpoint is synchronous, so a caller cannot make the server hold
// a request open for minutes retrying a target it knows is down.
const (
	analysisDefaultTimeoutSeconds = 10
	analysisMaxTimeoutSeconds     = 300
	analysisMaxRetries            = 5
	analysisDefaultRetrySeconds   = 5
	analysisMaxRetrySeconds       = 60
)

// AnalysisRequest is one on-demand analysis: the target (the same configuration
// a monitor carries, already parsed by the handler) plus the options the
// endpoint accepts.
type AnalysisRequest struct {
	// Type is the probe to run (http, keyword, tcp, dns, ssl, websocket).
	Type models.MonitorType
	// Config is the flat monitor configuration of the target. It is normalized
	// and validated exactly like the one of a monitor.
	Config models.MonitorConfig
	// TimeoutSeconds bounds a single probe (0 means the default).
	TimeoutSeconds int
	// Retries is how many extra attempts a DOWN uptime probe is given (0..5).
	Retries int
	// RetriesIntervalSeconds is the pause between two attempts (0 means the
	// default, which is only used when Retries > 0).
	RetriesIntervalSeconds int
	// UpsideDown inverts the uptime verdict (an unreachable target is up).
	UpsideDown bool
	// Checks is the list of probes to run. Empty means "the default list": the
	// uptime probe, plus the certificate one when the type can read a TLS chain.
	Checks []string
}

// AnalysisResult is the answer of one analysis: one section per probe that ran,
// each carrying its own outcome.
//
// A section is nil when its check was not requested, so a caller reads exactly
// what it asked for (and a client that only wants the certificate does not have
// to tell a stored certificate from an omitted one).
type AnalysisResult struct {
	// Target is the human readable target the probes ran against.
	Target string `json:"target"`
	// Type is the monitor type that was probed.
	Type models.MonitorType `json:"type"`
	// NodeID is the node that performed the work.
	NodeID string `json:"node_id"`
	// GeneratedAt is when the analysis ran.
	GeneratedAt time.Time `json:"generated_at"`
	// Checks is the resolved list of probes (the default is filled in).
	Checks []string `json:"checks"`

	Uptime      *AnalysisUptime      `json:"uptime,omitempty"`
	Certificate *AnalysisCertificate `json:"certificate,omitempty"`
	Domain      *AnalysisDomain      `json:"domain,omitempty"`
	Performance *AnalysisPerformance `json:"performance,omitempty"`

	// TimingsMS reports how long each probe and the whole analysis took, which
	// is what makes a slow answer explainable.
	TimingsMS AnalysisTimings `json:"timings_ms"`
}

// AnalysisTimings is the wall clock of the analysis. One field per probe, so the
// concurrent probes fill their own and no lock is needed.
type AnalysisTimings struct {
	Total       int64 `json:"total"`
	Uptime      int64 `json:"uptime,omitempty"`
	Certificate int64 `json:"certificate,omitempty"`
	Domain      int64 `json:"domain,omitempty"`
	Performance int64 `json:"performance,omitempty"`
}

// AnalysisUptime is the verdict of the monitor checker of the type.
type AnalysisUptime struct {
	// Status is the heartbeat status: up, down, pending or maintenance.
	Status string `json:"status"`
	// LatencyMS is how long the successful (or last) probe took.
	LatencyMS int64 `json:"latency_ms"`
	// StatusCode is the HTTP status of an http/keyword probe (0 when the type
	// reports none).
	StatusCode int `json:"status_code,omitempty"`
	// Message carries what the probe reported (the keyword that was missing, the
	// TLS error, the connection refusal).
	Message string `json:"message,omitempty"`
	// Attempts is how many probes were performed (1 plus the retries used).
	Attempts int `json:"attempts"`
}

// AnalysisCertificate is the TLS chain of the target, as the daily expiry job
// reads it.
type AnalysisCertificate struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Info is the observation. It is present even when Status is error: an
	// expired or self signed chain is exactly what the caller wants to see.
	Info *models.CertificateInfo `json:"info,omitempty"`
	// DaysLeft is the remaining validity, floored.
	DaysLeft int `json:"days_left"`
}

// AnalysisDomain is the registry expiration of the registrable domain of the
// target, as the daily expiry job reads it.
type AnalysisDomain struct {
	Status string             `json:"status"`
	Error  string             `json:"error,omitempty"`
	Info   *models.DomainInfo `json:"info,omitempty"`
}

// AnalysisPerformance is the PageSpeed Insights score of the page: one run per
// strategy the target asks for (mobile, desktop, or both).
type AnalysisPerformance struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Runs is one observation per strategy, each carrying its own status, so a
	// target measured on both runs reports a failed desktop run next to a
	// successful mobile one.
	Runs []*models.LighthouseResult `json:"runs,omitempty"`
}

// Analyze validates the request, runs the requested probes and returns the
// observations without storing a single one.
//
// Every probe is best effort: one that fails produces a section with status
// "error" and a message while the others still run (a target whose certificate
// cannot be read is still worth an uptime answer). Only an invalid request is
// an error, so a typo is a 400 and a target that refuses to answer is a 200.
func (s *AnalysisService) Analyze(ctx context.Context, req AnalysisRequest) (*AnalysisResult, error) {
	monitor, checks, err := s.prepare(req)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	result := &AnalysisResult{
		Target:      notify.MonitorTarget(monitor),
		Type:        monitor.Type,
		NodeID:      s.nodeID(),
		GeneratedAt: started.UTC(),
		Checks:      checks,
	}

	// One goroutine per probe: they are independent outbound calls, so the
	// answer takes as long as the slowest one instead of the sum. Every probe
	// writes its own section and its own timing field, so the fan out needs no
	// lock; a probe never returns an error, it reports it in its section.
	var group sync.WaitGroup
	if containsCheck(checks, AnalysisCheckUptime) {
		group.Add(1)
		go func() {
			defer group.Done()
			probe := time.Now()
			result.Uptime = s.runUptime(ctx, monitor)
			result.TimingsMS.Uptime = time.Since(probe).Milliseconds()
		}()
	}
	if containsCheck(checks, AnalysisCheckCertificate) {
		group.Add(1)
		go func() {
			defer group.Done()
			probe := time.Now()
			result.Certificate = s.runCertificate(ctx, monitor)
			result.TimingsMS.Certificate = time.Since(probe).Milliseconds()
		}()
	}
	if containsCheck(checks, AnalysisCheckDomain) {
		group.Add(1)
		go func() {
			defer group.Done()
			probe := time.Now()
			result.Domain = s.runDomain(ctx, monitor)
			result.TimingsMS.Domain = time.Since(probe).Milliseconds()
		}()
	}
	if containsCheck(checks, AnalysisCheckPerformance) {
		group.Add(1)
		go func() {
			defer group.Done()
			probe := time.Now()
			result.Performance = s.runPerformance(ctx, monitor)
			result.TimingsMS.Performance = time.Since(probe).Milliseconds()
		}()
	}
	group.Wait()

	result.TimingsMS.Total = time.Since(started).Milliseconds()
	s.log.Debug("analysis finished",
		"type", monitor.Type, "target", result.Target, "checks", checks,
		"total_ms", result.TimingsMS.Total)
	return result, nil
}

// prepare validates the request and builds the in-memory monitor the probes run
// against.
//
// It is the whole request validation: the configuration is normalized and
// checked with the very methods the monitor write path uses, so a target that
// passes the analysis cannot be refused by a monitor (and the other way round).
// The monitor that comes out is never persisted: it carries no name, no
// interval and no tags, because a probe does not need them.
func (s *AnalysisService) prepare(req AnalysisRequest) (*models.Monitor, []string, error) {
	if !req.Type.Valid() {
		return nil, nil, ErrBadRequest(i18n.CodeMonitorTypeInvalid,
			fmt.Sprintf("type must be one of %v", models.AllMonitorTypes()))
	}
	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = analysisDefaultTimeoutSeconds
	}
	if timeout < 1 || timeout > analysisMaxTimeoutSeconds {
		return nil, nil, ErrBadRequest(i18n.CodeValidation,
			fmt.Sprintf("timeout_seconds must be between 1 and %d", analysisMaxTimeoutSeconds))
	}
	if req.Retries < 0 || req.Retries > analysisMaxRetries {
		return nil, nil, ErrBadRequest(i18n.CodeValidation,
			fmt.Sprintf("retries must be between 0 and %d", analysisMaxRetries))
	}
	interval := req.RetriesIntervalSeconds
	if interval == 0 {
		interval = analysisDefaultRetrySeconds
	}
	if interval < 1 || interval > analysisMaxRetrySeconds {
		return nil, nil, ErrBadRequest(i18n.CodeValidation,
			fmt.Sprintf("retries_interval_seconds must be between 1 and %d", analysisMaxRetrySeconds))
	}

	config := req.Config
	config.Normalize(req.Type)
	if problem := config.Validate(req.Type); problem != "" {
		return nil, nil, ErrBadRequest(i18n.CodeMonitorConfig, problem)
	}

	monitor := &models.Monitor{
		Name:                   "analyze",
		Type:                   req.Type,
		Active:                 true,
		TimeoutSeconds:         timeout,
		Retries:                req.Retries,
		RetriesIntervalSeconds: interval,
		UpsideDown:             req.UpsideDown,
		Config:                 config,
	}
	checks, err := resolveChecks(req.Type, req.Checks, supportFor(monitor))
	if err != nil {
		return nil, nil, err
	}

	// The watch switches are implied by the checks: the probes read them (the
	// certificate target, the lighthouse targets), so a caller asking for the
	// certificate of a page does not have to know that a monitor would carry
	// cert_watch=true for it.
	monitor.CertWatch = containsCheck(checks, AnalysisCheckCertificate)
	monitor.DomainWatch = containsCheck(checks, AnalysisCheckDomain)
	monitor.LighthouseEnabled = containsCheck(checks, AnalysisCheckPerformance)
	return monitor, checks, nil
}

// analysisSupport reports which of the optional checks the TARGET can actually
// answer, as opposed to which ones its type allows.
//
// The distinction matters for the implicit list: an http monitor pointing at an
// http:// url supports a certificate check as a type but has no TLS endpoint to
// read one from, so the default list would otherwise carry a section that can
// only say "there is no certificate here".
type analysisSupport struct {
	// Certificate is true when a TLS handshake can be performed against the
	// target (an https/wss url, or a ssl host).
	Certificate bool
	// Page is true when the target is an absolute http(s) url PageSpeed
	// Insights can audit.
	Page bool
}

// supportFor inspects the target of an in-memory monitor.
//
// expiry.CertificateTargetFor reads the watch switch, so it is asked with the
// switch on and left alone: this is a question, not a decision (prepare sets the
// switches from the resolved checks right after).
func supportFor(monitor *models.Monitor) analysisSupport {
	monitor.CertWatch = true
	_, certificate := expiry.CertificateTargetFor(monitor)
	monitor.CertWatch = false
	_, page := lighthouse.NormalizeURL(monitor.Config.URL)
	return analysisSupport{
		Certificate: certificate,
		Page:        monitor.Type.SupportsLighthouse() && page,
	}
}

// resolveChecks validates the requested checks and applies the default list.
//
// The default is the uptime probe plus, when the target really has one, the TLS
// certificate: both are cheap and both describe the target as it is right now.
// The domain lookup and the PageSpeed Insights run are never implicit (one costs
// a registry query, the other an API quota unit).
func resolveChecks(monitorType models.MonitorType, raw []string, support analysisSupport) ([]string, error) {
	if len(raw) == 0 {
		checks := []string{AnalysisCheckUptime}
		if support.Certificate {
			checks = append(checks, AnalysisCheckCertificate)
		}
		return checks, nil
	}
	checks := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, entry := range raw {
		check := strings.ToLower(strings.TrimSpace(entry))
		if !validAnalysisCheck(check) {
			return nil, ErrBadRequest(i18n.CodeValidation,
				fmt.Sprintf("checks must be one of %s", strings.Join(analysisChecks, ", ")))
		}
		if seen[check] {
			continue
		}
		if problem := checkProblem(check, monitorType, support); problem != "" {
			return nil, ErrBadRequest(errorCodeForCheck(check), problem)
		}
		seen[check] = true
		checks = append(checks, check)
	}
	return checks, nil
}

// checkProblem describes why a check cannot run against the target, or "" when
// it can. A check that is impossible is a 400: the caller asked for something
// the target does not have, which is a request problem and not an observation.
func checkProblem(check string, monitorType models.MonitorType, support analysisSupport) string {
	switch check {
	case AnalysisCheckCertificate:
		if !monitorType.SupportsCertificate() {
			return "the certificate check is only available for http, keyword, ssl and websocket targets"
		}
		if !support.Certificate {
			return "the certificate check needs an https:// or wss:// url, or a ssl host"
		}
	case AnalysisCheckDomain:
		if !monitorType.SupportsDomainWatch() {
			return "the domain check is not available for " + string(monitorType) + " targets"
		}
	case AnalysisCheckPerformance:
		if !monitorType.SupportsLighthouse() {
			return "the performance check is only available for http and keyword targets"
		}
		if !support.Page {
			return "the performance check needs an absolute http:// or https:// url"
		}
	}
	return ""
}

// errorCodeForCheck maps a check to the error code of the monitor write path it
// mirrors, so a client that already handles cert_watch or lighthouse_enabled
// recognizes the failure.
func errorCodeForCheck(check string) string {
	switch check {
	case AnalysisCheckCertificate:
		return i18n.CodeMonitorCert
	case AnalysisCheckDomain:
		return i18n.CodeMonitorDomain
	case AnalysisCheckPerformance:
		return i18n.CodeMonitorLighthouse
	}
	return i18n.CodeValidation
}

// validAnalysisCheck reports whether a check name is known.
func validAnalysisCheck(check string) bool {
	for _, known := range analysisChecks {
		if check == known {
			return true
		}
	}
	return false
}

// containsCheck reports whether a resolved check list includes a check.
func containsCheck(checks []string, check string) bool {
	for _, entry := range checks {
		if entry == check {
			return true
		}
	}
	return false
}

// runUptime probes the target with the checker of its type, applying the retry
// policy of the request.
//
// The retries follow the scheduler's rule: they only happen while the probe says
// DOWN (an up target needs no second look) and the pause is the interruptible
// one of the endpoint (a cancelled request stops waiting at once instead of
// holding the connection for the whole backoff).
func (s *AnalysisService) runUptime(ctx context.Context, monitor *models.Monitor) *AnalysisUptime {
	interval := time.Duration(monitor.RetriesIntervalSeconds) * time.Second
	result := checkers.Check(ctx, monitor, "")
	attempts := 1
	for result.Status == models.StatusDown && attempts <= monitor.Retries {
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return uptimeSection(result, attempts)
		}
		result = checkers.Check(ctx, monitor, "")
		attempts++
	}
	return uptimeSection(result, attempts)
}

// uptimeSection renders a checker result as the uptime section of the answer.
func uptimeSection(result checkers.Result, attempts int) *AnalysisUptime {
	return &AnalysisUptime{
		Status:     result.Status.String(),
		LatencyMS:  result.LatencyMS,
		StatusCode: result.StatusCode,
		Message:    result.Message,
		Attempts:   attempts,
	}
}

// runCertificate reads the TLS chain of the target through the same handshake
// the daily expiry job performs.
//
// A chain that fails to verify still comes back (ProbeCertificate returns the
// certificates carried by the error), so the section reports the failure AND the
// observation: an expired certificate is the answer, not a dead end.
func (s *AnalysisService) runCertificate(ctx context.Context, monitor *models.Monitor) *AnalysisCertificate {
	target, ok := expiry.CertificateTargetFor(monitor)
	if !ok {
		return &AnalysisCertificate{Status: analysisStatusError,
			Error: "the target has no TLS endpoint to read a certificate from"}
	}
	info, err := checkers.ProbeCertificate(ctx, target.Address(), target.ServerName, target.Insecure,
		time.Duration(monitor.TimeoutSeconds)*time.Second)
	if info == nil {
		return &AnalysisCertificate{Status: analysisStatusError, Error: probeMessage(err)}
	}
	info.CapturedByNode = s.nodeID()
	section := &AnalysisCertificate{Status: analysisStatusOK, Info: info, DaysLeft: info.DaysLeft}
	if err != nil {
		section.Status = analysisStatusError
		section.Error = probeMessage(err)
	}
	return section
}

// runDomain resolves the registry expiration of the registrable domain of the
// target through the shared resolver (RDAP first, WHOIS with the operator parser
// after), which is what the daily expiry job uses.
func (s *AnalysisService) runDomain(ctx context.Context, monitor *models.Monitor) *AnalysisDomain {
	if s.domains == nil || s.domains.Resolver() == nil {
		return &AnalysisDomain{Status: analysisStatusError, Error: "the domain resolver is not wired"}
	}
	domain := expiry.DomainName(monitor)
	if domain == "" {
		return &AnalysisDomain{Status: analysisStatusError, Error: "the target has no registrable domain to look up"}
	}
	info := s.domains.Resolver().Resolve(ctx, domain, nil)
	if info == nil {
		return &AnalysisDomain{Status: analysisStatusError, Error: "the lookup returned no observation"}
	}
	info.CheckedByNode = s.nodeID()
	if info.Status == models.DomainStatusOK {
		return &AnalysisDomain{Status: analysisStatusOK, Info: info}
	}
	message := strings.TrimSpace(info.Error)
	if message == "" {
		message = string(info.Status)
	}
	return &AnalysisDomain{Status: analysisStatusError, Error: message, Info: info}
}

// runPerformance scores the page of the target with PageSpeed Insights, once per
// run the strategy stands for (mobile, desktop, or both), through the paced
// measurement of the web performance service.
func (s *AnalysisService) runPerformance(ctx context.Context, monitor *models.Monitor) *AnalysisPerformance {
	if s.lighthouse == nil {
		return &AnalysisPerformance{Status: analysisStatusError, Error: "the web performance service is not wired"}
	}
	targets := lighthouse.TargetsFor(monitor, models.DefaultLighthouseStrategy)
	if len(targets) == 0 {
		return &AnalysisPerformance{Status: analysisStatusError,
			Error: "the target is not an absolute http(s) url PageSpeed Insights can audit"}
	}
	section := &AnalysisPerformance{}
	failures := []string{}
	for _, target := range targets {
		result, err := s.lighthouse.MeasureTarget(ctx, target)
		if err != nil {
			failures = append(failures, target.Strategy+": "+probeMessage(err))
			continue
		}
		section.Runs = append(section.Runs, result)
	}
	if len(section.Runs) == 0 {
		section.Status = analysisStatusError
		section.Error = strings.Join(failures, "; ")
		return section
	}
	// At least one run came back, so the section is an answer that mentions what
	// it could not measure (a "both" target whose desktop run was refused).
	section.Status = analysisStatusOK
	if len(failures) > 0 {
		section.Error = strings.Join(failures, "; ")
	}
	return section
}

// probeMessage renders a probe failure as a single line message.
func probeMessage(err error) string {
	if err == nil {
		return "the probe returned nothing"
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

// nodeID reports the node that performed the analysis.
func (s *AnalysisService) nodeID() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.NodeID
}
