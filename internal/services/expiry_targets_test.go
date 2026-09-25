package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// TestPlanExpiryTargetsDeduplicates is the promise of the admin target list: the
// worklist is the same deduplicated one the daily job runs, so 30 monitors on one
// endpoint (or 12 on one registrable domain) are ONE row.
func TestPlanExpiryTargetsDeduplicates(t *testing.T) {
	monitors := []*models.Monitor{
		{
			ID: 1, Name: "site", Type: models.MonitorTypeHTTP,
			CertWatch: true, DomainWatch: true,
			Config: models.MonitorConfig{URL: "https://www.example.com/health"},
		},
		{
			ID: 2, Name: "api", Type: models.MonitorTypeHTTP,
			CertWatch: true, DomainWatch: true,
			Config: models.MonitorConfig{URL: "https://app.example.com/health"},
		},
		{
			ID: 3, Name: "mail", Type: models.MonitorTypeSSL,
			CertWatch: true, DomainWatch: true,
			Config: models.MonitorConfig{Host: "10.0.0.1", Port: 8443, ServerName: "app.example.com"},
		},
		{ID: 4, Name: "paused", Type: models.MonitorTypeHTTP},
	}

	targets := planExpiryTargets(monitors).targets()

	byKind := map[ExpiryTargetKind]int{}
	for _, target := range targets {
		byKind[target.Kind]++
		if target.Label == "" {
			t.Fatalf("target %v has no label", target)
		}
	}
	if byKind[ExpiryTargetCertificate] != 3 {
		t.Fatalf("certificate targets = %d, want 3 (%v)", byKind[ExpiryTargetCertificate], targets)
	}
	if byKind[ExpiryTargetDomain] != 1 {
		t.Fatalf("domain targets = %d, want 1 (www/app share example.com)", byKind[ExpiryTargetDomain])
	}

	var domain *ExpiryTarget
	for i := range targets {
		if targets[i].Kind == ExpiryTargetDomain {
			domain = &targets[i]
		}
	}
	if domain.Key != "example.com" {
		t.Fatalf("domain target = %q, want example.com", domain.Key)
	}
	// The IP monitor has no registrable domain, so it is not part of it.
	if len(domain.Monitors) != 2 || domain.Monitors[0].ID != 1 || domain.Monitors[1].ID != 2 {
		t.Fatalf("domain monitors = %v, want the two example.com monitors", domain.Monitors)
	}
	// Certificates come first (kind order), then the domain.
	if targets[0].Kind != ExpiryTargetCertificate || targets[len(targets)-1].Kind != ExpiryTargetDomain {
		t.Fatalf("ordering = %v", targets)
	}
}

// TestPlanExpiryTargetsIsDeterministic keeps the admin table stable: the same
// monitors must always produce the same order.
func TestPlanExpiryTargetsIsDeterministic(t *testing.T) {
	monitors := []*models.Monitor{
		{ID: 1, Name: "a", Type: models.MonitorTypeHTTP, DomainWatch: true, Config: models.MonitorConfig{URL: "https://z.example.com"}},
		{ID: 2, Name: "b", Type: models.MonitorTypeHTTP, DomainWatch: true, Config: models.MonitorConfig{URL: "https://a.example.com"}},
		{ID: 3, Name: "c", Type: models.MonitorTypeHTTP, CertWatch: true, Config: models.MonitorConfig{URL: "https://b.example.com"}},
	}
	first := planExpiryTargets(monitors).targets()
	second := planExpiryTargets(monitors).targets()
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Kind != second[i].Kind || first[i].Key != second[i].Key {
			t.Fatalf("order %d differs: %v vs %v", i, first[i], second[i])
		}
	}
}

// TestPlanExpiryTargetsManualDate covers the escape hatch for the TLDs that
// publish no date: a manual date never splits a domain into two targets, it
// marks the SINGLE target of that domain (the write path mirrors the value to
// every monitor of the domain, so the two cannot disagree).
func TestPlanExpiryTargetsManualDate(t *testing.T) {
	manual := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	monitors := []*models.Monitor{
		{
			ID: 1, Name: "manual", Type: models.MonitorTypeHTTP, DomainWatch: true,
			DomainExpiresAt: &manual, Config: models.MonitorConfig{URL: "https://www.example.com"},
		},
		{
			ID: 2, Name: "lookup", Type: models.MonitorTypeHTTP, DomainWatch: true,
			Config: models.MonitorConfig{URL: "https://app.example.com"},
		},
	}

	plan := planExpiryTargets(monitors)
	targets := plan.targets()
	if len(targets) != 1 {
		t.Fatalf("targets = %v, want one for the domain", targets)
	}
	target := targets[0]
	if target.Kind != ExpiryTargetDomain || target.Key != "example.com" {
		t.Fatalf("unexpected target %v", target)
	}
	if !target.Manual {
		t.Fatalf("a domain with a manual date must be marked manual: %v", target)
	}
	if len(target.Monitors) != 2 {
		t.Fatalf("monitors = %v, want the two monitors of the domain", target.Monitors)
	}
	if date := plan.manualDate("example.com"); date == nil || !date.Equal(manual) {
		t.Fatalf("manualDate = %v, want %v", date, manual)
	}
}

// TestPlanExpiryTargetsIPHasNoDomain documents why an IP monitor never creates a
// registry target: a registry does not bill an address.
func TestPlanExpiryTargetsIPHasNoDomain(t *testing.T) {
	monitors := []*models.Monitor{
		{
			ID: 1, Name: "ip", Type: models.MonitorTypeTCP, DomainWatch: true,
			Config: models.MonitorConfig{Host: "10.0.0.1", Port: 443},
		},
	}
	plan := planExpiryTargets(monitors)
	if len(plan.domains) != 0 || len(plan.manual) != 0 {
		t.Fatalf("an IP must yield no domain target: %v %v", plan.domains, plan.manual)
	}
	if len(plan.certificates) != 0 {
		t.Fatalf("a tcp monitor has no certificate target either: %v", plan.certificates)
	}
}

// TestSummarizeExpiryTarget picks the number an operator acts on: the smallest
// remaining validity, or the first reported failure when nothing has a date.
func TestSummarizeExpiryTarget(t *testing.T) {
	target := ExpiryTarget{Monitors: []ExpiryTargetMonitor{
		{ID: 1, Status: string(models.DomainStatusOK), DaysLeft: 20},
		{ID: 2, Status: string(models.DomainStatusOK), DaysLeft: 5},
		{ID: 3, Status: string(models.DomainStatusError)},
	}}
	summarizeExpiryTarget(&target)
	if target.Status != string(models.DomainStatusOK) || target.DaysLeft == nil || *target.DaysLeft != 5 {
		t.Fatalf("summary = %q %v, want ok 5", target.Status, target.DaysLeft)
	}

	failing := ExpiryTarget{Monitors: []ExpiryTargetMonitor{
		{ID: 1}, {ID: 2, Status: string(models.DomainStatusUnsupported)},
	}}
	summarizeExpiryTarget(&failing)
	if failing.Status != string(models.DomainStatusUnsupported) || failing.DaysLeft != nil {
		t.Fatalf("summary = %q %v, want unsupported and no date", failing.Status, failing.DaysLeft)
	}

	empty := ExpiryTarget{Monitors: []ExpiryTargetMonitor{{ID: 1}}}
	summarizeExpiryTarget(&empty)
	if empty.Status != "" || empty.DaysLeft != nil {
		t.Fatalf("summary = %q %v, want empty", empty.Status, empty.DaysLeft)
	}
}

// TestRunTargetValidatesInput keeps the "refresh this row" action honest: a bad
// kind or an empty target is a 400 before anything touches the resolver.
func TestRunTargetValidatesInput(t *testing.T) {
	service := &ExpiryService{}
	cases := []struct {
		name string
		kind ExpiryTargetKind
		key  string
	}{
		{name: "unknown kind", kind: "banana", key: "example.com"},
		{name: "empty target", kind: ExpiryTargetDomain, key: "   "},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.RunTarget(context.Background(), testCase.kind, testCase.key)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want an *APIError", err)
			}
			if apiErr.Status != 400 || apiErr.Code != i18n.CodeValidation {
				t.Fatalf("error = %d %s, want 400 %s", apiErr.Status, apiErr.Code, i18n.CodeValidation)
			}
		})
	}
}
