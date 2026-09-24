package services

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/expiry"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// ExpiryTargetKind is the kind of thing the daily expiry job refreshes: the TLS
// certificate of an endpoint or the registry registration of a domain.
type ExpiryTargetKind string

// The two kinds of expiry target.
const (
	ExpiryTargetCertificate ExpiryTargetKind = "certificate"
	ExpiryTargetDomain      ExpiryTargetKind = "domain"
)

// ExpiryTargetMonitor is one monitor of a target, with the observation stored
// for it (empty when the monitor was never refreshed).
type ExpiryTargetMonitor struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status,omitempty"`
	DaysLeft int    `json:"days_left,omitempty"`
	// ExpiresAt is the certificate NotAfter or the registry expiration.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// ExpiryTarget is one deduplicated unit of work of the daily expiry job.
//
// It exists so the operator can see what a run would actually look up and
// refresh a single row, instead of only being able to fire the whole job: 30
// monitors on the same endpoint are ONE certificate target, and 12 monitors on
// app.example.com are ONE domain target.
type ExpiryTarget struct {
	Kind ExpiryTargetKind `json:"kind"`
	// Key is the deduplication identity: "host:port|sni" for a certificate and
	// the registrable domain (eTLD+1) for a registration.
	Key string `json:"key"`
	// Label is what the admin table shows (the dial address, the domain).
	Label string `json:"label"`
	// Address and ServerName describe a certificate target (empty for a domain).
	Address    string `json:"address,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	// Domain is the registrable domain (empty for a certificate target).
	Domain string `json:"domain,omitempty"`
	// Manual reports a domain target whose only source is the date typed in the
	// monitor form: it needs no network lookup at all.
	Manual bool `json:"manual"`
	// Monitors are the monitors that share this target.
	Monitors []ExpiryTargetMonitor `json:"monitors"`
	// Status, DaysLeft, ExpiresAt and CheckedAt summarise the target with the
	// most urgent observation of its monitors (see summarizeExpiryTarget).
	Status    string     `json:"status,omitempty"`
	DaysLeft  *int       `json:"days_left,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// expiryPlan is the deduplicated worklist of one run, built from the monitor
// list alone: it is pure, which is what lets the daily job and the admin view
// share exactly the same definition of "the same target".
type expiryPlan struct {
	certificates map[string]expiry.CertificateTarget
	certWatchers map[string][]*models.Monitor
	domains      map[string][]*models.Monitor
	manual       map[string][]*models.Monitor
}

// planExpiryTargets groups the watchers by the target they share.
//
// A monitor with a manual date lands in the manual group: the operator typed
// the truth (some TLDs publish no date) and no lookup is needed.
func planExpiryTargets(monitors []*models.Monitor) expiryPlan {
	plan := expiryPlan{
		certificates: map[string]expiry.CertificateTarget{},
		certWatchers: map[string][]*models.Monitor{},
		domains:      map[string][]*models.Monitor{},
		manual:       map[string][]*models.Monitor{},
	}
	for _, monitor := range monitors {
		if monitor == nil {
			continue
		}
		if monitor.CertWatch {
			if target, ok := expiry.CertificateTargetFor(monitor); ok {
				key := target.Key()
				plan.certificates[key] = target
				plan.certWatchers[key] = append(plan.certWatchers[key], monitor)
			}
		}
		if !monitor.DomainWatch {
			continue
		}
		domain := expiry.DomainFor(monitor)
		if domain == "" {
			continue
		}
		if monitor.DomainExpiresAt != nil && !monitor.DomainExpiresAt.IsZero() {
			plan.manual[domain] = append(plan.manual[domain], monitor)
			continue
		}
		plan.domains[domain] = append(plan.domains[domain], monitor)
	}
	return plan
}

// targets flattens the plan into the ordered list the admin view renders. The
// order is deterministic (kind, then key) so the table never reshuffles itself.
func (p expiryPlan) targets() []ExpiryTarget {
	out := make([]ExpiryTarget, 0, len(p.certificates)+len(p.domains)+len(p.manual))
	for key, target := range p.certificates {
		out = append(out, ExpiryTarget{
			Kind:       ExpiryTargetCertificate,
			Key:        key,
			Label:      target.Address(),
			Address:    target.Address(),
			ServerName: target.ServerName,
			Monitors:   targetMonitors(p.certWatchers[key]),
		})
	}
	for domain, monitors := range p.domains {
		out = append(out, ExpiryTarget{
			Kind:     ExpiryTargetDomain,
			Key:      domain,
			Label:    domain,
			Domain:   domain,
			Monitors: targetMonitors(monitors),
		})
	}
	for domain, monitors := range p.manual {
		out = append(out, ExpiryTarget{
			Kind:     ExpiryTargetDomain,
			Key:      domain,
			Label:    domain,
			Domain:   domain,
			Manual:   true,
			Monitors: targetMonitors(monitors),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// targetMonitors converts the monitors of a group into the payload shape,
// ordered by id so the same plan always renders the same way.
func targetMonitors(monitors []*models.Monitor) []ExpiryTargetMonitor {
	out := make([]ExpiryTargetMonitor, 0, len(monitors))
	for _, monitor := range monitors {
		out = append(out, ExpiryTargetMonitor{ID: monitor.ID, Name: monitor.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// summarizeExpiryTarget copies the most urgent observation of the target into
// the target itself: the smallest remaining validity among the monitors that
// have a date (status ok), or the first reported status when none has one.
func summarizeExpiryTarget(target *ExpiryTarget) {
	var chosen *ExpiryTargetMonitor
	for i := range target.Monitors {
		monitor := &target.Monitors[i]
		if monitor.Status == "" {
			continue
		}
		ok := monitor.Status == string(models.DomainStatusOK)
		if chosen == nil {
			chosen = monitor
			continue
		}
		chosenOk := chosen.Status == string(models.DomainStatusOK)
		switch {
		case ok && !chosenOk:
			chosen = monitor
		case ok && chosenOk && monitor.DaysLeft < chosen.DaysLeft:
			chosen = monitor
		}
	}
	if chosen == nil {
		return
	}
	target.Status = chosen.Status
	if chosen.Status == string(models.DomainStatusOK) {
		days := chosen.DaysLeft
		target.DaysLeft = &days
		target.ExpiresAt = chosen.ExpiresAt
	}
	target.CheckedAt = chosen.CheckedAt
}

// Targets returns the deduplicated worklist of the job with the observation
// stored for each monitor. It never opens a socket (the admin view is a
// preview) and it is what the daily run itself iterates.
func (s *ExpiryService) Targets(ctx context.Context) ([]ExpiryTarget, error) {
	watchers, err := s.monitors.ExpiryWatchers(ctx)
	if err != nil {
		return nil, err
	}
	targets := planExpiryTargets(watchers).targets()
	if len(targets) == 0 {
		return targets, nil
	}

	ids := make([]uint, 0, len(watchers))
	for _, monitor := range watchers {
		ids = append(ids, monitor.ID)
	}
	certificates := map[uint]*models.MonitorCertificate{}
	domains := map[uint]*models.MonitorDomain{}
	if s.certificates != nil {
		if certificates, err = s.certificates.ByMonitorIDs(ctx, ids); err != nil {
			return nil, err
		}
	}
	if s.domains != nil {
		if domains, err = s.domains.ByMonitorIDs(ctx, ids); err != nil {
			return nil, err
		}
	}

	for i := range targets {
		target := &targets[i]
		for j := range target.Monitors {
			state := &target.Monitors[j]
			switch target.Kind {
			case ExpiryTargetCertificate:
				if row, ok := certificates[state.ID]; ok && row != nil {
					state.Status = string(models.DomainStatusOK)
					state.DaysLeft = row.DaysLeft
					state.ExpiresAt = timePtr(row.NotAfter)
					state.CheckedAt = timePtr(row.CapturedAt)
				}
			case ExpiryTargetDomain:
				if row, ok := domains[state.ID]; ok && row != nil {
					state.Status = row.Status
					state.DaysLeft = row.DaysLeft
					state.ExpiresAt = timePtr(row.ExpiresAt)
					state.CheckedAt = timePtr(row.CheckedAt)
				}
			}
		}
		summarizeExpiryTarget(target)
	}
	return targets, nil
}

// RunTarget refreshes a single target of the worklist and reports how many
// monitors it updated. It is the per-row "refresh" action of the admin view: it
// goes through the same resolver, the same rate limit and the same evaluation
// as the daily job, so a manual refresh cannot produce a different observation.
func (s *ExpiryService) RunTarget(ctx context.Context, kind ExpiryTargetKind, key string) (int, error) {
	key = strings.TrimSpace(key)
	if kind != ExpiryTargetCertificate && kind != ExpiryTargetDomain {
		return 0, ErrBadRequest(i18n.CodeValidation, "kind must be certificate or domain")
	}
	if key == "" {
		return 0, ErrBadRequest(i18n.CodeValidation, "target is required")
	}
	run, err := s.prepare(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	timeout := time.Duration(run.settings.TimeoutSeconds) * time.Second

	if kind == ExpiryTargetCertificate {
		target, ok := run.plan.certificates[key]
		if !ok {
			return 0, ErrNotFound(i18n.CodeNotFound, "no certificate target "+key)
		}
		refreshed := s.refreshCertificate(ctx, target, run.plan.certWatchers[key], now, timeout)
		s.log.Info("expiry target refreshed", "kind", kind, "target", key, "monitors", refreshed)
		return refreshed, nil
	}

	monitors, manual := run.plan.domains[key], false
	if len(monitors) == 0 {
		monitors, manual = run.plan.manual[key], true
	}
	if len(monitors) == 0 {
		return 0, ErrNotFound(i18n.CodeNotFound, "no domain target "+key)
	}
	refreshed := s.refreshDomain(ctx, key, monitors, manual, now)
	s.log.Info("expiry target refreshed", "kind", kind, "target", key, "manual", manual, "monitors", refreshed)
	return refreshed, nil
}

// timePtr converts a stored timestamp into the nullable shape of the payload.
func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}
