package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
)

// listNotifications returns every channel.
func (h *Container) listNotifications(c *gin.Context) {
	notifications, err := h.Notifications.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, notifications)
}

// getNotification returns a single channel.
func (h *Container) getNotification(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	notification, err := h.Notifications.Get(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, notification)
}

// createNotification stores a new channel.
func (h *Container) createNotification(c *gin.Context) {
	var notification models.Notification
	if !bindJSON(c, &notification) {
		return
	}
	// The default webhook body template is applied when the operator left it
	// empty, so the payload is useful out of the box.
	if notification.Type == models.NotificationWebhook && notification.Config.Webhook != nil &&
		notification.Config.Webhook.BodyTemplate == "" {
		notification.Config.Webhook.BodyTemplate = models.DefaultWebhookBodyTemplate
	}
	if err := h.Notifications.Create(c.Request.Context(), &notification); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	// The response carries the computed fields, `monitor_ids` included (an array,
	// `[]` when the channel is linked to nothing, never null): the card of the
	// admin page reads that field unguarded, and a null threw `null.length` inside
	// its render, which Vue answers with an empty placeholder (the card vanished).
	created, err := h.Notifications.Get(c.Request.Context(), notification.ID)
	if err != nil {
		api.Created(c, notification)
		return
	}
	api.Created(c, created)
}

// updateNotification saves an existing channel.
func (h *Container) updateNotification(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var notification models.Notification
	if !bindJSON(c, &notification) {
		return
	}
	notification.ID = id
	if err := h.Notifications.Update(c.Request.Context(), &notification); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	// Same contract as the create: the write never touches the links (they are
	// stored on the monitor side), so the saved channel is read back instead of
	// echoing a payload that carries no `monitor_ids` (see createNotification).
	saved, err := h.Notifications.Get(c.Request.Context(), id)
	if err != nil {
		api.OK(c, notification)
		return
	}
	api.OK(c, saved)
}

// deleteNotification removes a channel.
func (h *Container) deleteNotification(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.Notifications.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// testNotification sends a test message through the channel.
func (h *Container) testNotification(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	entry, err := h.Notifications.Test(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, entry)
}

// notificationLogs returns the delivery history.
func (h *Container) notificationLogs(c *gin.Context) {
	logs, err := h.Notifications.Logs(c.Request.Context(), queryInt(c, "limit", 100))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, logs)
}
