package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/ivancarlosti/up/internal/models"
)

// Engine delivers notifications through shoutrrr.
type Engine struct {
	log     *slog.Logger
	shout   types.StdLogger
	timeout time.Duration
	client  *http.Client
}

// NewEngine builds the delivery engine.
func NewEngine(log *slog.Logger, timeout time.Duration) *Engine {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Engine{
		log:     log,
		shout:   &stdLogger{log: log},
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

// Send dispatches a message using the channel configuration.
func (e *Engine) Send(notification *models.Notification, msg Message) error {
	switch notification.Type {
	case models.NotificationSMTP:
		return e.SendSMTP(notification, msg)
	case models.NotificationWebhook:
		return e.SendWebhook(notification, msg)
	case models.NotificationSlack:
		return e.SendSlack(notification, msg)
	case models.NotificationDiscord:
		return e.SendDiscord(notification, msg)
	case models.NotificationTelegram:
		return e.SendTelegram(notification, msg)
	}
	return fmt.Errorf("unsupported notification type %q", notification.Type)
}

// SendSMTP delivers an e-mail through shoutrrr's smtp service.
func (e *Engine) SendSMTP(notification *models.Notification, msg Message) error {
	if notification.Config.SMTP == nil {
		return errors.New("smtp configuration is missing")
	}
	cfg := notification.Config.SMTP
	return e.SendSMTPTo(notification, msg, cfg.To, subjectFor(cfg, msg))
}

// SendSMTPTo delivers an e-mail to one explicit recipient with an explicit
// subject, keeping the transport settings of the channel. It is what the status
// page subscription mails use: each subscriber is its own recipient, while the
// channel still owns the server, credentials and sender.
func (e *Engine) SendSMTPTo(notification *models.Notification, msg Message, recipient, subject string) error {
	if notification.Config.SMTP == nil {
		return errors.New("smtp configuration is missing")
	}
	cfg := notification.Config.SMTP
	if strings.TrimSpace(recipient) == "" {
		return errors.New("the e-mail recipient is missing")
	}
	serviceURL := buildSMTPURLTo(cfg, subject, recipient)

	serviceRouter, err := e.newRouter(serviceURL)
	if err != nil {
		return fmt.Errorf("initializing the smtp service: %w", err)
	}

	body := msg.Body(cfg.UseHTML)
	// Only the parameters the smtp service knows about are forwarded: the body
	// itself travels in the message argument, and an unknown key (for example
	// "message") makes shoutrrr reject the whole send.
	params := types.Params{"title": subject}
	return joinSendErrors(serviceRouter.Send(body, &params))
}

// SendWebhook delivers the rendered body through shoutrrr's generic service.
func (e *Engine) SendWebhook(notification *models.Notification, msg Message) error {
	if notification.Config.Webhook == nil {
		return errors.New("webhook configuration is missing")
	}
	cfg := notification.Config.Webhook

	body, err := msg.RenderBody(cfg.BodyTemplate)
	if err != nil {
		return err
	}
	if err := checkWebhookBody(cfg.ContentType, body); err != nil {
		return err
	}
	serviceURL, err := buildWebhookURL(cfg)
	if err != nil {
		return err
	}

	serviceRouter, err := e.newRouter(serviceURL)
	if err != nil {
		return fmt.Errorf("initializing the webhook service: %w", err)
	}

	// With no shoutrrr template configured, params["message"] is sent verbatim
	// as the request body; the Content-Type is forced through a custom header.
	params := types.Params{"message": body, "title": msg.Title}
	return joinSendErrors(serviceRouter.Send(body, &params))
}

// checkWebhookBody refuses a body the target cannot parse. A webhook that
// declares a JSON content type is answered with a 4xx by the receiving end when
// the payload is not valid JSON (n8n answers 422 "Failed to parse request
// body"), while shoutrrr only reports "server returned unexpected response
// status code" - the status line, not the cause. Validating the rendered body
// here turns that into an error the delivery log can explain.
//
// An empty content type means application/json: that is the default of
// shoutrrr's generic service. A blank body is always accepted, because a GET or
// HEAD request carries none and a target that never parses the body is free to
// receive anything.
func checkWebhookBody(contentType, body string) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return nil
	}
	var probe any
	err = json.Unmarshal([]byte(body), &probe)
	if err == nil {
		return nil
	}
	return fmt.Errorf("the rendered webhook body is not valid JSON (content_type %q): %s - "+
		"a value probably carries a quote, a backslash or a newline - render it with "+
		"{{json .Field}} instead of \"{{.Field}}\"", contentType, describeBodyError(body, err))
}

// describeBodyError names where a body stops parsing: the offending byte and a
// short excerpt around it. A template has sixteen placeholders on average and the
// rendered body is one long line, so "it is not valid JSON" alone leaves the
// operator to find the value that broke it by hand - while the delivery log has
// room for the answer. The excerpt is single line (Go's %q escapes the control
// characters) and bounded, so one runaway value cannot fill the log.
func describeBodyError(body string, err error) string {
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || syntax.Offset <= 0 || int(syntax.Offset) > len(body) {
		return err.Error()
	}
	// Offset points just past the byte that could not be parsed, so the excerpt
	// is centred on it with a little more context behind than ahead.
	return fmt.Sprintf("%s at byte %d, near %q", syntax.Error(), syntax.Offset,
		bodyExcerpt(body, int(syntax.Offset)))
}

// bodyExcerpt is a window of at most bodyExcerptWidth bytes around offset, cut on
// a rune boundary so a multi-byte character renders instead of a broken byte.
func bodyExcerpt(body string, offset int) string {
	const bodyExcerptWidth = 64
	start := offset - bodyExcerptWidth/2
	if start < 0 {
		start = 0
	}
	end := start + bodyExcerptWidth
	if end > len(body) {
		end = len(body)
		if start = end - bodyExcerptWidth; start < 0 {
			start = 0
		}
	}
	return string([]rune(body[start:end]))
}

// SendSlack delivers a message through shoutrrr's slack service.
func (e *Engine) SendSlack(notification *models.Notification, msg Message) error {
	if notification.Config.Slack == nil {
		return errors.New("slack configuration is missing")
	}
	serviceURL, err := buildSlackURL(notification.Config.Slack)
	if err != nil {
		return err
	}
	return e.sendChat("slack", serviceURL, msg)
}

// SendDiscord delivers a message through shoutrrr's discord service.
func (e *Engine) SendDiscord(notification *models.Notification, msg Message) error {
	if notification.Config.Discord == nil {
		return errors.New("discord configuration is missing")
	}
	serviceURL, err := buildDiscordURL(notification.Config.Discord)
	if err != nil {
		return err
	}
	return e.sendChat("discord", serviceURL, msg)
}

// SendTelegram delivers a message through shoutrrr's telegram service.
func (e *Engine) SendTelegram(notification *models.Notification, msg Message) error {
	if notification.Config.Telegram == nil {
		return errors.New("telegram configuration is missing")
	}
	serviceURL, err := buildTelegramURL(notification.Config.Telegram)
	if err != nil {
		return err
	}
	return e.sendChat("telegram", serviceURL, msg)
}

// sendChat delivers the plain text body through one chat service.
//
// The chat services take the body as their message argument and accept no send
// time parameter other than "title": an unknown key (for example "message")
// makes shoutrrr reject the whole send, so only the title is forwarded.
func (e *Engine) sendChat(service, serviceURL string, msg Message) error {
	serviceRouter, err := e.newRouter(serviceURL)
	if err != nil {
		return fmt.Errorf("initializing the %s service: %w", service, err)
	}
	params := types.Params{"title": msg.Title}
	return joinSendErrors(serviceRouter.Send(msg.Text(), &params))
}

// newRouter creates a shoutrrr router for a single service URL.
func (e *Engine) newRouter(serviceURL string) (*router.ServiceRouter, error) {
	return router.NewWithOptions(e.shout, types.SenderOptions{
		HTTPClient: e.client,
		Timeout:    e.timeout,
	}, serviceURL)
}

// stdLogger adapts slog to the shoutrrr logging interface. Service logs are
// kept at debug level so a verbose notification service never floods the
// application log.
type stdLogger struct {
	log *slog.Logger
}

func (l *stdLogger) Print(args ...any) {
	l.log.Debug("shoutrrr", "message", strings.TrimSpace(fmt.Sprint(args...)))
}
func (l *stdLogger) Printf(format string, args ...any) {
	l.log.Debug("shoutrrr", "message", fmt.Sprintf(format, args...))
}
func (l *stdLogger) Println(args ...any) {
	l.log.Debug("shoutrrr", "message", strings.TrimSpace(fmt.Sprintln(args...)))
}

func joinSendErrors(errs []error) error {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return errors.New(strings.Join(messages, "; "))
}

func subjectFor(cfg *models.SMTPConfig, msg Message) string {
	prefix := strings.TrimSpace(cfg.SubjectPrefix)
	if prefix == "" {
		return msg.Title
	}
	return prefix + " " + msg.Title
}
