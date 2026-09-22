package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivancarlosti/up/internal/models"
)

// BadgeSVG renders a small shields.io style badge with the overall status of a
// public status page, ready to be embedded in a README:
//
//	![status](https://up.example.com/api/public/status/my-status/badge.svg)
func (s *StatusPageService) BadgeSVG(ctx context.Context, slug string) (string, error) {
	page, err := s.PublicPayload(ctx, slug, false)
	if err != nil {
		return "", err
	}
	label := strings.TrimSpace(page.Title)
	if label == "" {
		label = "status"
	}
	label = truncateText(label, 22)
	status := string(page.OverallStatus)

	color := "#2ea043" // green
	switch page.OverallStatus {
	case models.AggregateDown:
		color = "#d1242f"
	case models.AggregateDegraded:
		color = "#d29922"
	case models.AggregatePending, models.AggregateUnknown:
		color = "#6e7781"
	}

	// Widths follow the classic shields.io formula (7px per character).
	labelWidth := 6 + len(label)*7
	statusWidth := 10 + len(status)*7
	total := labelWidth + statusWidth
	labelMid := labelWidth / 2
	statusMid := labelWidth + statusWidth/2

	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">
  <title>%s: %s</title>
  <linearGradient id="s" x2="0" y2="100%%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <clipPath id="r"><rect width="%d" height="20" rx="3" fill="#fff"/></clipPath>
  <g clip-path="url(#r)">
    <rect width="%d" height="20" fill="#555"/>
    <rect x="%d" width="%d" height="20" fill="%s"/>
    <rect width="%d" height="20" fill="url(#s)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">
    <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
    <text x="%d" y="14">%s</text>
    <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
    <text x="%d" y="14">%s</text>
  </g>
</svg>`,
		total, escapeSVG(label), escapeSVG(status),
		escapeSVG(label), escapeSVG(status),
		total,
		labelWidth, labelWidth, statusWidth, color, total,
		labelMid, escapeSVG(label), labelMid, escapeSVG(label),
		statusMid, escapeSVG(status), statusMid, escapeSVG(status),
	), nil
}

func truncateText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

// escapeSVG escapes the characters that would break the badge markup.
func escapeSVG(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}
