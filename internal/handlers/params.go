package handlers

import (
	"math"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
)

// pathID parses the :id parameter.
func pathID(c *gin.Context) (uint, bool) {
	raw := c.Param("id")
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		api.WriteError(c, 400, i18n.CodeValidation, "invalid id "+raw)
		return 0, false
	}
	// `uint` is 32 bits wide on some platforms: refuse an id the local type
	// cannot hold instead of letting the conversion wrap around.
	if value > math.MaxUint32 {
		api.WriteError(c, 400, i18n.CodeValidation, "invalid id "+raw)
		return 0, false
	}
	return uint(value), true
}

// queryInt reads an integer query parameter.
func queryInt(c *gin.Context, key string, fallback int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// queryBool reads a boolean query parameter.
func queryBool(c *gin.Context, key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// queryString reads a trimmed string query parameter.
func queryString(c *gin.Context, key string) string {
	return strings.TrimSpace(c.Query(key))
}

// bindJSON decodes the request body and reports a readable error.
func bindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		api.BadRequest(c, "invalid request body: "+err.Error())
		return false
	}
	return true
}
