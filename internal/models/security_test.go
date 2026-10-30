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

// TestRecaptchaSettingsMinScore pins the threshold rules the admin card and the
// Site Verify check rely on: an absent value (a client that predates the field,
// or a fresh install) becomes Google's recommended 0.5 instead of 0, which would
// accept every visitor, and a value outside the supported band cannot be stored.
func TestRecaptchaSettingsMinScore(t *testing.T) {
	fresh := RecaptchaSettings{}
	fresh.Normalize()
	if fresh.MinScore != DefaultRecaptchaMinScore {
		t.Fatalf("empty payload normalized to %v, want %v", fresh.MinScore, DefaultRecaptchaMinScore)
	}

	kept := RecaptchaSettings{MinScore: 0.7}
	kept.Normalize()
	if kept.MinScore != 0.7 {
		t.Fatalf("an explicit threshold was overwritten: %v", kept.MinScore)
	}

	pair := RecaptchaSettings{ClientID: "6Le-site", ClientSecret: "6Le-secret"}
	for _, score := range []float64{MinRecaptchaMinScore, DefaultRecaptchaMinScore, MaxRecaptchaMinScore} {
		settings := pair
		settings.MinScore = score
		if problem := settings.Validate(); problem != "" {
			t.Fatalf("score %v rejected: %q", score, problem)
		}
	}
	for _, score := range []float64{0.05, 0.95, 1} {
		settings := pair
		settings.MinScore = score
		if problem := settings.Validate(); problem == "" {
			t.Fatalf("score %v accepted, want a validation problem", score)
		}
	}

	// The pair stays the switch: no threshold can enable a captcha without keys.
	onlyScore := RecaptchaSettings{MinScore: 0.5}
	if onlyScore.Enabled() {
		t.Fatal("a threshold alone must not enable the captcha")
	}
}

// TestRecaptchaActions guards the names shared with the frontend: they travel
// inside the token and are compared server side, so renaming one without the
// other would reject every submit of that form.
func TestRecaptchaActions(t *testing.T) {
	if RecaptchaActionLogin != "login" {
		t.Fatalf("login action = %q", RecaptchaActionLogin)
	}
	if RecaptchaActionStatusSubscribe != "status_page_subscribe" {
		t.Fatalf("subscription action = %q", RecaptchaActionStatusSubscribe)
	}
	if RecaptchaActionLogin == RecaptchaActionStatusSubscribe {
		t.Fatal("the two forms must not share an action")
	}
}
