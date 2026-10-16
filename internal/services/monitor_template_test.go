package services

import (
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestTemplateClone is the guard of the template clone: the definition (probe
// config, defaults, switch) travels with the copy, the identity does not, and the
// two optional attachments follow their options.
func TestTemplateClone(t *testing.T) {
	source := &models.MonitorTemplate{
		ID:          7,
		UUID:        "source-uuid",
		Name:        "website | address",
		Description: "the usual probe",
		Type:        models.MonitorTypeHTTP,
		Propagate:   false,
		Config:      models.MonitorConfig{Method: "HEAD", AcceptedStatusCodes: "200-299"},
		Defaults: models.TemplateDefaults{
			IntervalSeconds: 120,
			NotificationIDs: []uint{3, 4},
		},
		LinkScope: models.TemplateLinkScope{Kind: models.TemplateScopeTags, Tags: []string{"prod"}},
	}

	// The full copy keeps everything but the identity.
	full := templateClone(source, "website | address (copy)", MonitorTemplateCloneOptions{CopyChannels: true, CopyScope: true})
	if full.Name != "website | address (copy)" || full.Description != source.Description ||
		full.Type != source.Type || full.Propagate != false || full.Config.Method != "HEAD" {
		t.Fatalf("the definition did not travel: %+v", full)
	}
	if len(full.Defaults.NotificationIDs) != 2 {
		t.Fatalf("the channels must travel with copy_channels: %+v", full.Defaults.NotificationIDs)
	}
	if full.LinkScope.Kind != models.TemplateScopeTags || len(full.LinkScope.Tags) != 1 {
		t.Fatalf("the scope must travel with copy_scope: %+v", full.LinkScope)
	}
	if full.ID != 0 || full.UUID != "" || full.Revision != 0 || full.OriginNodeID != "" {
		t.Fatalf("the identity must be left to BeforeCreate: %+v", full)
	}

	// Dropping the two switches drops the two attachments (the scope falls back
	// to the neutral default on Normalize, never to the source decision).
	bare := templateClone(source, "copy", MonitorTemplateCloneOptions{})
	if bare.Defaults.NotificationIDs != nil {
		t.Fatalf("copy_channels=false must clear the channels: %+v", bare.Defaults.NotificationIDs)
	}
	if !bare.LinkScope.IsZero() {
		t.Fatalf("copy_scope=false must leave the scope unset: %+v", bare.LinkScope)
	}
	// The copy must not share the channel slice with the source: a later edit of
	// either row must never reach the other through the backing array.
	if &full.Defaults.NotificationIDs[0] == &source.Defaults.NotificationIDs[0] {
		t.Fatal("the channels must be copied, not aliased")
	}
}

// edit: the JSON columns must be written as marshalled strings, because GORM
// applies "serializer:json" to model fields and never to raw map values (a
// struct value reaches the driver and fails with
// "unsupported type models.MonitorConfig, a struct").
func TestTemplateUpdateColumns(t *testing.T) {
	template := &models.MonitorTemplate{
		Name:        "website | address",
		Description: "the usual probe",
		Type:        models.MonitorTypeHTTP,
		Propagate:   true,
		Config:      models.MonitorConfig{Method: "HEAD", AcceptedStatusCodes: "200-299"},
		Defaults:    models.TemplateDefaults{IntervalSeconds: 120, NotificationIDs: []uint{3}},
	}

	columns, err := templateUpdateColumns(template)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, key := range []string{"config", "defaults"} {
		value, ok := columns[key]
		if !ok {
			t.Fatalf("%s is missing from the update", key)
		}
		if _, isString := value.(string); !isString {
			t.Fatalf("%s must be a marshalled string, got %T", key, value)
		}
	}

	// The values must round-trip: the API and the UI read them back from the row.
	var config models.MonitorConfig
	if err := json.Unmarshal([]byte(columns["config"].(string)), &config); err != nil {
		t.Fatalf("the config is not valid JSON: %v", err)
	}
	if config.Method != "HEAD" || config.AcceptedStatusCodes != "200-299" {
		t.Fatalf("config = %+v", config)
	}
	var defaults models.TemplateDefaults
	if err := json.Unmarshal([]byte(columns["defaults"].(string)), &defaults); err != nil {
		t.Fatalf("the defaults are not valid JSON: %v", err)
	}
	if defaults.IntervalSeconds != 120 || len(defaults.NotificationIDs) != 1 {
		t.Fatalf("defaults = %+v", defaults)
	}

	// The switch of the link feature has to travel with the update, otherwise it
	// silently keeps the column default.
	if value, ok := columns["propagate"]; !ok || value != true {
		t.Fatalf("propagate = %v (present = %t), want true", value, ok)
	}
	if _, ok := columns["revision"]; !ok {
		t.Fatal("the revision must be bumped by the update")
	}

	// The failure this guards against happens in database/sql, which is where GORM
	// hands a raw map value to the driver: a struct is rejected with
	// "sql: converting argument $1 type: unsupported type models.MonitorConfig, a
	// struct", the 500 that every template edit used to answer.
	converter := driver.DefaultParameterConverter
	if _, err := converter.ConvertValue(models.MonitorConfig{}); err == nil {
		t.Fatal("a struct is expected to be rejected as a driver argument")
	}
	for _, key := range []string{"config", "defaults"} {
		if _, err := converter.ConvertValue(columns[key]); err != nil {
			t.Fatalf("%s is not a valid driver argument: %v", key, err)
		}
	}
}
