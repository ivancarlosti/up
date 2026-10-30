package i18n

import (
	"fmt"
	"html"
	"strings"
)

// SubscriptionEmailData is everything the subscription mails interpolate. It is
// deliberately a plain struct: the mail builder lives in services, and i18n only
// owns the words.
type SubscriptionEmailData struct {
	PageTitle      string
	MonitorName    string
	Detail         string
	StatusURL      string
	UnsubscribeURL string
	Time           string
}

// SubscriptionEmail is one rendered mail (subject, plain text and HTML).
type SubscriptionEmail struct {
	Subject string
	Text    string
	HTML    string
}

// Subscription mail kinds.
const (
	SubscriptionMailConfirm = "confirm"
	SubscriptionMailDown    = "down"
	SubscriptionMailUp      = "up"
)

type subscriptionTexts struct {
	ConfirmSubject string
	ConfirmLead    string
	ConfirmAction  string
	DownSubject    string
	DownLead       string
	UpSubject      string
	UpLead         string
	Detail         string
	Time           string
	ViewPage       string
	Unsubscribe    string
	Ignore         string
	Footer         string
}

func replaceAll(template string, pairs ...string) string {
	if len(pairs)%2 != 0 {
		return template
	}
	values := make([]string, 0, len(pairs))
	for i := 0; i < len(pairs); i += 2 {
		values = append(values, pairs[i], pairs[i+1])
	}
	return strings.NewReplacer(values...).Replace(template)
}

// SubscriptionEmailMessage renders the confirmation, down or up mail in the
// subscriber's locale. An unknown locale falls back to en-US rather than
// returning an error: a mail that cannot be rendered must never block a
// subscription.
func SubscriptionEmailMessage(locale, kind string, data SubscriptionEmailData) SubscriptionEmail {
	texts, ok := subscriptionTextsByLocale[locale]
	if !ok {
		texts = subscriptionTextsByLocale["en-US"]
	}
	fields := []string{
		"{page}", data.PageTitle,
		"{monitor}", data.MonitorName,
		"{detail}", data.Detail,
		"{time}", data.Time,
		"{status_url}", data.StatusURL,
		"{unsubscribe_url}", data.UnsubscribeURL,
	}
	tr := func(value string) string { return replaceAll(value, fields...) }

	var subject, lead, action string
	switch kind {
	case SubscriptionMailDown:
		subject, lead, action = tr(texts.DownSubject), tr(texts.DownLead), tr(texts.ViewPage)
	case SubscriptionMailUp:
		subject, lead, action = tr(texts.UpSubject), tr(texts.UpLead), tr(texts.ViewPage)
	default:
		subject, lead, action = tr(texts.ConfirmSubject), tr(texts.ConfirmLead), tr(texts.ConfirmAction)
	}

	lines := []string{lead}
	if kind != SubscriptionMailConfirm {
		if data.Detail != "" {
			lines = append(lines, tr(texts.Detail))
		}
		if data.Time != "" {
			lines = append(lines, tr(texts.Time))
		}
	}
	lines = append(lines, action+": "+data.StatusURL)
	lines = append(lines, tr(texts.Unsubscribe)+": "+data.UnsubscribeURL)
	if kind == SubscriptionMailConfirm {
		lines = append(lines, tr(texts.Ignore))
	}
	lines = append(lines, tr(texts.Footer))

	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}

	var h strings.Builder
	h.WriteString(`<div style="font-family:system-ui,Segoe UI,Helvetica,Arial,sans-serif;font-size:14px;color:#111">`)
	fmt.Fprintf(&h, "<p>%s</p>", html.EscapeString(lead))
	if kind != SubscriptionMailConfirm {
		if data.Detail != "" {
			fmt.Fprintf(&h, "<p>%s</p>", html.EscapeString(tr(texts.Detail)))
		}
		if data.Time != "" {
			fmt.Fprintf(&h, "<p>%s</p>", html.EscapeString(tr(texts.Time)))
		}
	}
	fmt.Fprintf(&h, `<p><a href="%s">%s</a></p>`,
		html.EscapeString(data.StatusURL), html.EscapeString(action))
	if kind == SubscriptionMailConfirm {
		fmt.Fprintf(&h, "<p>%s</p>", html.EscapeString(tr(texts.Ignore)))
	}
	fmt.Fprintf(&h, `<p style="color:#666;font-size:12px"><a href="%s">%s</a><br>%s</p>`,
		html.EscapeString(data.UnsubscribeURL), html.EscapeString(tr(texts.Unsubscribe)),
		html.EscapeString(tr(texts.Footer)))
	h.WriteString(`</div>`)

	return SubscriptionEmail{Subject: subject, Text: b.String(), HTML: h.String()}
}
