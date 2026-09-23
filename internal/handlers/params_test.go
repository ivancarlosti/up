package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestPathIDRejectsIDsUintCannotHold pins the bounds check of the :id
// parameter: the value is parsed on 64 bits and the conversion to `uint` (32
// bits wide on some platforms) must not silently wrap around.
func TestPathIDRejectsIDsUintCannotHold(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/api/monitors/:id", func(c *gin.Context) {
		id, ok := pathID(c)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": id})
	})

	cases := []struct {
		name   string
		target string
		status int
		want   uint
	}{
		{"a regular id", "/api/monitors/42", http.StatusOK, 42},
		{"the largest id uint holds", "/api/monitors/4294967295", http.StatusOK, 4294967295},
		{"one above it", "/api/monitors/4294967296", http.StatusBadRequest, 0},
		{"zero", "/api/monitors/0", http.StatusBadRequest, 0},
		{"negative", "/api/monitors/-1", http.StatusBadRequest, 0},
		{"not a number", "/api/monitors/abc", http.StatusBadRequest, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if recorder.Code != tc.status {
				t.Fatalf("%s: status = %d, want %d", tc.target, recorder.Code, tc.status)
			}
			if tc.status != http.StatusOK {
				return
			}
			var body struct {
				ID uint `json:"id"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("could not decode the response: %v", err)
			}
			if body.ID != tc.want {
				t.Errorf("%s: id = %d, want %d", tc.target, body.ID, tc.want)
			}
		})
	}
}
