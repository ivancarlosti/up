package models

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CertificateInfo is the TLS certificate observed by a probe: what the operator
// wants to see (who issued it, when it expires) and what the watcher uses to
// decide about the reminders.
type CertificateInfo struct {
	Subject    string    `json:"subject"`
	Issuer     string    `json:"issuer"`
	Serial     string    `json:"serial,omitempty"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	DNSNames   []string  `json:"dns_names,omitempty"`
	DaysLeft   int       `json:"days_left"`
	CapturedAt time.Time `json:"captured_at"`
	// CapturedByNode is the cluster node that read the certificate.
	CapturedByNode string `json:"captured_by_node,omitempty"`
}

// Expired reports whether the certificate is already past its NotAfter.
func (c *CertificateInfo) Expired() bool {
	if c == nil {
		return false
	}
	return c.NotAfter.Before(c.CapturedAt)
}

// DaysLeft returns the whole days between now and the moment, flooring the
// result: a certificate that expires in 12 hours has 0 days left and one already
// expired has a negative value (-1 hour is -1 day, never 0).
func DaysLeft(notAfter, now time.Time) int {
	remaining := notAfter.UTC().Sub(now.UTC())
	const day = 24 * time.Hour
	if remaining < 0 && remaining%day != 0 {
		return int(remaining/day) - 1
	}
	return int(remaining / day)
}

// MonitorCertificate is the stored certificate state of a monitor.
//
// It lives in its own table (and not in `config`) because it is runtime state: it
// is rewritten by every probe that reads the certificate, and the notification
// bookkeeping (which thresholds were already alerted, on which day) has to
// survive those rewrites.
type MonitorCertificate struct {
	MonitorID      uint      `gorm:"primaryKey" json:"monitor_id"`
	Subject        string    `gorm:"size:255" json:"subject"`
	Issuer         string    `gorm:"size:255" json:"issuer"`
	Serial         string    `gorm:"size:120" json:"serial"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
	DNSNames       string    `gorm:"size:500" json:"dns_names"`
	DaysLeft       int       `json:"days_left"`
	CapturedAt     time.Time `json:"captured_at"`
	CapturedByNode string    `gorm:"size:64" json:"captured_by_node"`
	// NotifiedDays is the CSV list of warn thresholds already alerted ("30,7").
	// It is what guarantees one alert per threshold even when the process was
	// down on the exact day.
	NotifiedDays string `gorm:"size:120" json:"notified_days"`
	// LastNotifiedDay is the day bucket (unix/86400) of the last certificate
	// notification, which is what makes the reminder daily.
	LastNotifiedDay int64 `json:"last_notified_day"`
}

// TableName keeps the table name stable.
func (MonitorCertificate) TableName() string { return "monitor_certificates" }

// Info converts the row into the payload exposed to the API and the UI.
func (c *MonitorCertificate) Info() *CertificateInfo {
	if c == nil {
		return nil
	}
	names := []string{}
	for _, name := range strings.Split(c.DNSNames, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	return &CertificateInfo{
		Subject:        c.Subject,
		Issuer:         c.Issuer,
		Serial:         c.Serial,
		NotBefore:      c.NotBefore,
		NotAfter:       c.NotAfter,
		DNSNames:       names,
		DaysLeft:       c.DaysLeft,
		CapturedAt:     c.CapturedAt,
		CapturedByNode: c.CapturedByNode,
	}
}

// NotifiedThresholds returns the thresholds already alerted.
func (c *MonitorCertificate) NotifiedThresholds() []int {
	days, _ := ParseCertWarnDays(c.NotifiedDays)
	return days
}

// DefaultCertWarnDays is the list used when the monitor leaves the field empty:
// a certificate that expires within a month starts producing reminders.
const DefaultCertWarnDays = "30,14,7,1"

// ParseCertWarnDays parses the operator provided list of "days before expiry".
//
// The list is free form on purpose (any numbers, any order, e.g. "7,6,5,30"):
// it is only cleaned up internally (zeroes, duplicates and negatives dropped,
// sorted ascending) and never rewritten in the database, so the UI keeps showing
// exactly what the operator typed.
func ParseCertWarnDays(raw string) ([]int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	days := []int{}
	seen := map[int]bool{}
	for _, part := range strings.Split(trimmed, ",") {
		piece := strings.TrimSpace(part)
		if piece == "" {
			continue
		}
		value, err := strconv.Atoi(piece)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number of days", piece)
		}
		if value < 0 {
			return nil, fmt.Errorf("%d is negative", value)
		}
		if value > 3650 {
			return nil, fmt.Errorf("%d days is too far in the future (maximum 3650)", value)
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		days = append(days, value)
	}
	sort.Ints(days)
	return days, nil
}

// WarnDaysOrDefault returns the configured thresholds or the default list.
func WarnDaysOrDefault(raw string) []int {
	days, err := ParseCertWarnDays(raw)
	if err != nil || len(days) == 0 {
		fallback, _ := ParseCertWarnDays(DefaultCertWarnDays)
		return fallback
	}
	return days
}
