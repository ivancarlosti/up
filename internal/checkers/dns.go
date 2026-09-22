package checkers

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/ivancarlosti/up/internal/models"
)

// dnsTypeMap maps the record type names accepted by the UI to the miekg/dns
// query types.
var dnsTypeMap = map[string]uint16{
	"A":     dns.TypeA,
	"AAAA":  dns.TypeAAAA,
	"CNAME": dns.TypeCNAME,
	"MX":    dns.TypeMX,
	"TXT":   dns.TypeTXT,
	"NS":    dns.TypeNS,
	"SOA":   dns.TypeSOA,
}

// checkDNS resolves a record through a specific resolver and optionally
// validates an expected value.
//
// Invert check semantics:
//   - with an expected value: the monitor is up when the value is NOT matched.
//   - without an expected value: the monitor is up when the name does NOT
//     resolve (useful to detect unwanted DNS records).
func checkDNS(ctx context.Context, monitor *models.Monitor) Result {
	cfg := monitor.Config

	recordType, ok := dnsTypeMap[strings.ToUpper(cfg.RecordType)]
	if !ok {
		return Result{Status: models.StatusDown, Message: "unsupported record type " + cfg.RecordType}
	}

	resolver := strings.TrimSpace(cfg.ResolverServer)
	if resolver == "" {
		resolver = "1.1.1.1"
	}
	if _, _, err := net.SplitHostPort(resolver); err != nil {
		resolver = net.JoinHostPort(resolver, "53")
	}

	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(cfg.Hostname), recordType)
	query.RecursionDesired = true

	client := &dns.Client{Timeout: timeoutFromContext(ctx)}
	started := time.Now()
	response, _, err := client.ExchangeContext(ctx, query, resolver)
	latency := time.Since(started).Milliseconds()

	if err != nil {
		return Result{
			Status:    models.StatusDown,
			LatencyMS: latency,
			Message:   fmt.Sprintf("query failed on %s: %s", resolver, describeRequestError(err)),
		}
	}
	if response.Rcode != dns.RcodeSuccess {
		return Result{
			Status:    models.StatusDown,
			LatencyMS: latency,
			Message:   fmt.Sprintf("%s from %s", dns.RcodeToString[response.Rcode], resolver),
		}
	}

	values := extractDNSValues(response, recordType)
	if len(values) == 0 {
		return Result{
			Status:    models.StatusDown,
			LatencyMS: latency,
			Message:   fmt.Sprintf("no %s records returned by %s", cfg.RecordType, resolver),
		}
	}
	joined := strings.Join(values, ", ")

	if strings.TrimSpace(cfg.ExpectedValue) == "" {
		if cfg.InvertCheck {
			return Result{
				Status:    models.StatusDown,
				LatencyMS: latency,
				Message:   fmt.Sprintf("records found (%s) but invert check is enabled", joined),
			}
		}
		return Result{
			Status:    models.StatusUp,
			LatencyMS: latency,
			Message:   fmt.Sprintf("%s %s -> %s", cfg.RecordType, cfg.Hostname, joined),
		}
	}

	expected := strings.ToLower(strings.TrimSpace(cfg.ExpectedValue))
	matched := false
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), expected) {
			matched = true
			break
		}
	}
	if cfg.InvertCheck {
		matched = !matched
	}

	if !matched {
		return Result{
			Status:    models.StatusDown,
			LatencyMS: latency,
			Message:   fmt.Sprintf("expected %q in %s, got %s", cfg.ExpectedValue, cfg.RecordType, joined),
		}
	}
	return Result{
		Status:    models.StatusUp,
		LatencyMS: latency,
		Message:   fmt.Sprintf("%q matched in %s", cfg.ExpectedValue, joined),
	}
}

// extractDNSValues renders the answers of the requested type as strings.
func extractDNSValues(response *dns.Msg, recordType uint16) []string {
	values := make([]string, 0, len(response.Answer))
	for _, answer := range response.Answer {
		switch record := answer.(type) {
		case *dns.A:
			if recordType == dns.TypeA {
				values = append(values, record.A.String())
			}
		case *dns.AAAA:
			if recordType == dns.TypeAAAA {
				values = append(values, record.AAAA.String())
			}
		case *dns.CNAME:
			if recordType == dns.TypeCNAME {
				values = append(values, strings.TrimSuffix(record.Target, "."))
			}
		case *dns.NS:
			if recordType == dns.TypeNS {
				values = append(values, strings.TrimSuffix(record.Ns, "."))
			}
		case *dns.MX:
			if recordType == dns.TypeMX {
				values = append(values, fmt.Sprintf("%d %s", record.Preference, strings.TrimSuffix(record.Mx, ".")))
			}
		case *dns.TXT:
			if recordType == dns.TypeTXT {
				values = append(values, strings.Join(record.Txt, " "))
			}
		case *dns.SOA:
			if recordType == dns.TypeSOA {
				values = append(values, strings.TrimSpace(record.String()))
			}
		}
	}
	return values
}
