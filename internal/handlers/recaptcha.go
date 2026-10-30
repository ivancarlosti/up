package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// adminRecaptcha returns the Google reCAPTCHA configuration of the login form
// and of the public subscription form (Admin > Settings, "Login protection").
//
// The secret is part of the answer: this is the authenticated admin API, the
// same one that already returns the stored settings map, and the card has to
// show what is stored so a key can be rotated.
func (h *Container) adminRecaptcha(c *gin.Context) {
	api.OK(c, h.recaptchaPayload())
}

// updateAdminRecaptcha stores both keys and the v3 score threshold. An empty
// pair switches the captcha off; a half filled pair is rejected, because it
// would either hand out tokens the server cannot verify or demand a token no
// client could mint.
func (h *Container) updateAdminRecaptcha(c *gin.Context) {
	var payload models.RecaptchaSettings
	if !bindJSON(c, &payload) {
		return
	}
	payload.Normalize()
	if problem := payload.Validate(); problem != "" {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation, problem)
		return
	}
	if err := h.Settings.SetRecaptcha(c.Request.Context(), payload); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, h.recaptchaPayload())
}

// recaptchaPayload is what both endpoints answer: the stored pair, the score
// threshold (a v3 answer that does not reach it is rejected) and the state the
// card renders. "enabled" is derived from the pair on purpose, there is no third
// setting that could contradict it.
func (h *Container) recaptchaPayload() gin.H {
	captcha := h.Settings.Recaptcha()
	return gin.H{
		"client_id":     captcha.ClientID,
		"client_secret": captcha.ClientSecret,
		"min_score":     captcha.MinScore,
		"enabled":       captcha.Enabled(),
	}
}
