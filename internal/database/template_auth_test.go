package database

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStripTemplateAuth is the pure half of the boot pass: it must remove every
// credential a template row may still store, must keep the rest of the probe
// configuration, and must leave a row it has nothing to say about byte for byte as
// it is (that is what makes the pass idempotent).
func TestStripTemplateAuth(t *testing.T) {
	// A stored configuration with a basic user, a password and a bearer token: the
	// three go, the rest stays.
	stored := []byte(`{"url":"https://example.com","method":"POST","auth_type":"basic",` +
		`"basic_user":"operator","basic_pass":"hunter2","bearer_token":"token","accepted_status_codes":"200"}`)
	clean, changed, err := stripTemplateAuth(stored)
	if err != nil {
		t.Fatalf("stripTemplateAuth: %v", err)
	}
	if !changed {
		t.Fatal("a configuration with credentials must be reported as changed")
	}
	var got map[string]any
	if err := json.Unmarshal(clean, &got); err != nil {
		t.Fatalf("the cleaned configuration is not valid JSON: %v", err)
	}
	for _, field := range []string{"basic_user", "basic_pass", "bearer_token"} {
		if value, ok := got[field]; ok && value != "" {
			t.Fatalf("%s survived: %v", field, value)
		}
	}
	if got["auth_type"] != "none" {
		t.Fatalf("auth_type = %v, want none", got["auth_type"])
	}
	if got["url"] != "https://example.com" || got["method"] != "POST" || got["accepted_status_codes"] != "200" {
		t.Fatalf("the probe options must be kept: %v", got)
	}
	// Running the pass a second time (the second boot) changes nothing.
	if _, again, err := stripTemplateAuth(clean); err != nil || again {
		t.Fatalf("the pass is not idempotent: changed=%v err=%v", again, err)
	}

	// A credential free row is returned untouched, not re-encoded: the pass must
	// not rewrite a row it has nothing to say about.
	untouched := []byte(`{"url":"https://example.com","auth_type":"none"}`)
	if out, changed, err := stripTemplateAuth(untouched); err != nil || changed || string(out) != string(untouched) {
		t.Fatalf("a credential free row was rewritten: changed=%v out=%s err=%v", changed, out, err)
	}

	// An auth type that means a credential counts even without a value (a half
	// filled form used to be storable), and a dangling token alone counts too.
	for _, in := range []string{`{"auth_type":"bearer"}`, `{"basic_pass":"hunter2"}`, `{"basic_user":"operator"}`} {
		if _, changed, err := stripTemplateAuth([]byte(in)); err != nil || !changed {
			t.Fatalf("%s must be cleaned (changed=%v err=%v)", in, changed, err)
		}
	}

	// An empty column is nothing to clean, and a corrupt one is an error the
	// caller has to see rather than a silent skip.
	if out, changed, err := stripTemplateAuth(nil); err != nil || changed || out != nil {
		t.Fatalf("an empty column: changed=%v out=%v err=%v", changed, out, err)
	}
	if _, _, err := stripTemplateAuth([]byte("{not json")); err == nil {
		t.Fatal("a corrupt configuration must be reported")
	}
}

// TestConfigHasAuth keeps the predicate that decides whether a row is rewritten
// honest: only a credential (or the auth type that carries one) counts.
func TestConfigHasAuth(t *testing.T) {
	cases := []struct {
		config map[string]any
		want   bool
	}{
		{map[string]any{}, false},
		{map[string]any{"auth_type": "none"}, false},
		{map[string]any{"auth_type": ""}, false},
		{map[string]any{"basic_user": ""}, false},
		{map[string]any{"auth_type": "basic"}, true},
		{map[string]any{"auth_type": "bearer"}, true},
		{map[string]any{"basic_user": "operator"}, true},
		{map[string]any{"basic_pass": "hunter2"}, true},
		{map[string]any{"bearer_token": "token"}, true},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.config)
		if err != nil {
			t.Fatalf("encoding %v: %v", tc.config, err)
		}
		// The predicate is unexported and takes the model, so go through the
		// public entry point that the pass uses.
		_, changed, err := stripTemplateAuth(raw)
		if err != nil {
			t.Fatalf("stripTemplateAuth(%s): %v", raw, err)
		}
		if changed != tc.want {
			t.Errorf("stripTemplateAuth(%s) changed = %v, want %v", strings.TrimSpace(string(raw)), changed, tc.want)
		}
	}
}
