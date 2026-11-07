package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// storedNotification is one webhook channel as this pass reads it: the id to
// update and the raw JSON column.
//
// The column is read raw on purpose. `models.Notification.AfterFind` already
// upgrades the template in memory, so a pass that loaded the rows through the
// model would compare the healed value with itself and never find anything to
// write back.
type storedNotification struct {
	ID     uint
	Config []byte
}

// BackfillWebhookBodyTemplates rewrites the webhook body template that a channel
// still stores as the default of an older release.
//
// Why it is needed: until the json helper reached DefaultWebhookBodyTemplate
// (see its doc), the block filled into the dialog wrote its values raw inside a
// quoted string - `"message": "{{.Message}}"` - so a check detail that carries a
// quote, a backslash or a newline rendered a body the target could not parse and
// the alert was lost (a keyword check always renders `keyword "hello" matched`).
// Normalize only fills an EMPTY template, so such a row kept the old block
// verbatim forever. AfterFind already upgrades it on every read, which is what
// makes an existing channel deliver again without waiting for this pass; the
// pass is what puts the upgraded block on disk (and in every backup of it).
//
// It only replaces a stored SHIPPED default (see
// models.UpgradeWebhookBodyTemplate): a template the operator wrote, broken or
// not, is never rewritten. Idempotent - a row that already holds the current
// block is left byte for byte as it is - and the revision is intentionally NOT
// advanced, for the same reason as the template auth backfill: this is not a new
// version of the row, it is the repair of a value an older release wrote, and
// every node runs the same pass on its own copy at boot.
func BackfillWebhookBodyTemplates(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	var rows []storedNotification
	if err := db.WithContext(ctx).
		Raw("SELECT id, config FROM notifications WHERE type = ?", string(models.NotificationWebhook)).
		Scan(&rows).Error; err != nil {
		return fmt.Errorf("reading the webhook channels: %w", err)
	}
	upgraded := 0
	for _, row := range rows {
		next, changed, err := upgradeWebhookBody(row.Config)
		if err != nil {
			return fmt.Errorf("cleaning the config of notification %d: %w", row.ID, err)
		}
		if !changed {
			continue
		}
		if err := db.WithContext(ctx).Exec(
			"UPDATE notifications SET config = ? WHERE id = ?", string(next), row.ID).Error; err != nil {
			return fmt.Errorf("cleaning the config of notification %d: %w", row.ID, err)
		}
		upgraded++
	}
	if upgraded == 0 {
		log.Debug("webhook body template backfill: nothing to do")
	} else {
		log.Info("webhook body templates upgraded to the escaped block", "rows", upgraded)
	}
	return nil
}

// upgradeWebhookBody returns the stored configuration with the current body
// template and whether it had to be replaced.
//
// It is pure so the rule can be tested without a database, and it is deliberately
// conservative: a channel of another type, an absent webhook block, an empty
// template (Normalize fills it on the next save) and a template that is not a
// shipped default all come back untouched.
func upgradeWebhookBody(stored []byte) ([]byte, bool, error) {
	if len(stored) == 0 {
		// A row whose config column is NULL or empty: the model fills the
		// defaults on the next write, there is nothing to upgrade here.
		return stored, false, nil
	}
	var config models.NotificationConfig
	if err := json.Unmarshal(stored, &config); err != nil {
		return nil, false, fmt.Errorf("decoding the stored configuration: %w", err)
	}
	if config.Webhook == nil {
		return stored, false, nil
	}
	upgraded := models.UpgradeWebhookBodyTemplate(config.Webhook.BodyTemplate)
	if upgraded == config.Webhook.BodyTemplate {
		return stored, false, nil
	}
	config.Webhook.BodyTemplate = upgraded
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, false, fmt.Errorf("encoding the upgraded configuration: %w", err)
	}
	return encoded, true, nil
}
