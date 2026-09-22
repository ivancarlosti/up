package notify

import (
	"errors"
	"fmt"
	"log/slog"
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
	}
	return fmt.Errorf("unsupported notification type %q", notification.Type)
}

// SendSMTP delivers an e-mail through shoutrrr's smtp service.
func (e *Engine) SendSMTP(notification *models.Notification, msg Message) error {
	if notification.Config.SMTP == nil {
		return errors.New("smtp configuration is missing")
	}
	cfg := notification.Config.SMTP
	serviceURL := buildSMTPURL(cfg, msg)

	serviceRouter, err := e.newRouter(serviceURL)
	if err != nil {
		return fmt.Errorf("initializing the smtp service: %w", err)
	}

	body := msg.Body(cfg.UseHTML)
	// Only the parameters the smtp service knows about are forwarded: the body
	// itself travels in the message argument, and an unknown key (for example
	// "message") makes shoutrrr reject the whole send.
	params := types.Params{"title": subjectFor(cfg, msg)}
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
