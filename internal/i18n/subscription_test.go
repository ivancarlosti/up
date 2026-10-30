package i18n

import (
	"strings"
	"testing"
)

func TestSubscriptionMailIsRenderedInEveryLocale(t *testing.T) {
	for _, locale := range []string{"en-US", "pt-BR", "es-MX", "fr-FR", "zh-CN", "hi-IN", "ar-SA"} {
		for _, kind := range []string{SubscriptionMailConfirm, SubscriptionMailDown, SubscriptionMailUp} {
			mail := SubscriptionEmailMessage(locale, kind, SubscriptionEmailData{
				PageTitle:      "Example status",
				MonitorName:    "API",
				Detail:         "connection refused",
				StatusURL:      "https://status.example.com/status/example",
				UnsubscribeURL: "https://status.example.com/unsubscribe?token=abc",
				Time:           "Mon, 01 Jan 2026 00:00:00 UTC",
			})
			if strings.TrimSpace(mail.Subject) == "" || strings.TrimSpace(mail.Text) == "" || strings.TrimSpace(mail.HTML) == "" {
				t.Fatalf("%s/%s rendered an empty mail", locale, kind)
			}
			if !strings.Contains(mail.Text, "https://status.example.com/status/example") {
				t.Errorf("%s/%s lost the status link", locale, kind)
			}
			if !strings.Contains(mail.Text, "https://status.example.com/unsubscribe?token=abc") {
				t.Errorf("%s/%s lost the unsubscribe link", locale, kind)
			}
		}
	}
}

func TestSubscriptionMailFallsBackToEnglish(t *testing.T) {
	mail := SubscriptionEmailMessage("xx-XX", SubscriptionMailDown, SubscriptionEmailData{
		PageTitle: "Page", MonitorName: "Monitor", StatusURL: "https://example.com",
		UnsubscribeURL: "https://example.com/u",
	})
	if !strings.Contains(mail.Subject, "is down") {
		t.Fatalf("an unknown locale did not fall back to en-US: %q", mail.Subject)
	}
}
