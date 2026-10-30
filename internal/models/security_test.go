package models

import "testing"

// TestRecaptchaSettingsEnablement fixes the rule the handlers rely on: the
// captcha is required only when the site key and the secret are both stored,
// which is also what GET /api/settings and /api/auth/session report.
func TestRecaptchaSettingsEnablement(t *testing.T) {
	cases := []struct {
		name     string
		settings RecaptchaSettings
		enabled  bool
	}{
		{"empty pair", RecaptchaSettings{}, false},
		{"site key only", RecaptchaSettings{ClientID: "6Le-site"}, false},
		{"secret only", RecaptchaSettings{ClientSecret: "6Le-secret"}, false},
		{"both halves", RecaptchaSettings{ClientID: "6Le-site", ClientSecret: "6Le-secret"}, true},
	}
	for _, testCase := range cases {
		if got := testCase.settings.Enabled(); got != testCase.enabled {
			t.Fatalf("%s: Enabled() = %t, want %t", testCase.name, got, testCase.enabled)
		}
	}
}

// TestRecaptchaSettingsValidate rejects the half filled pair: a site key
// without a secret renders a widget whose token can never be verified, and a
// secret without a site key demands a token no client can mint.
func TestRecaptchaSettingsValidate(t *testing.T) {
	if problem := (RecaptchaSettings{}).Validate(); problem != "" {
		t.Fatalf("an empty pair must be valid (captcha off), got %q", problem)
	}
	if problem := (RecaptchaSettings{ClientID: "6Le-site", ClientSecret: "6Le-secret"}).Validate(); problem != "" {
		t.Fatalf("a complete pair must be valid, got %q", problem)
	}
	if problem := (RecaptchaSettings{ClientID: "6Le-site"}).Validate(); problem == "" {
		t.Fatal("a site key without a secret must be rejected")
	}
	if problem := (RecaptchaSettings{ClientSecret: "6Le-secret"}).Validate(); problem == "" {
		t.Fatal("a secret without a site key must be rejected")
	}
}

// TestRecaptchaSettingsNormalize covers what an operator pastes from the
// reCAPTCHA console: the keys are copied by hand, so stray spaces and the
// newline of a copy must not count as a value nor as half a configuration.
func TestRecaptchaSettingsNormalize(t *testing.T) {
	settings := RecaptchaSettings{ClientID: "  6Le-site\n", ClientSecret: "\t6Le-secret  "}
	settings.Normalize()
	if settings.ClientID != "6Le-site" || settings.ClientSecret != "6Le-secret" {
		t.Fatalf("normalized = %q/%q", settings.ClientID, settings.ClientSecret)
	}

	blank := RecaptchaSettings{ClientID: "   ", ClientSecret: "\n"}
	blank.Normalize()
	if blank.Enabled() {
		t.Fatal("whitespace alone must not enable the captcha")
	}
	if problem := blank.Validate(); problem != "" {
		t.Fatalf("two blank halves are an empty pair, got %q", problem)
	}

	half := RecaptchaSettings{ClientID: "6Le-site", ClientSecret: "  \n"}
	half.Normalize()
	if problem := half.Validate(); problem == "" {
		t.Fatal("a blank secret must be rejected, not trimmed away silently")
	}
}
