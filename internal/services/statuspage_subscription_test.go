package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

func TestNormalizeSubscriptionEmail(t *testing.T) {
	valid := []string{"person@example.com", "  PERSON@Example.COM "}
	for _, input := range valid {
		got, err := normalizeSubscriptionEmail(input)
		if err != nil {
			t.Fatalf("normalizeSubscriptionEmail(%q) failed: %v", input, err)
		}
		if got != "person@example.com" {
			t.Errorf("normalizeSubscriptionEmail(%q) = %q", input, got)
		}
	}
	invalid := []string{"", "person", "person@", "@example.com", "Name <person@example.com>", "two words@example.com", "\"a,b\"@example.com"}
	for _, input := range invalid {
		if _, err := normalizeSubscriptionEmail(input); err == nil {
			t.Errorf("normalizeSubscriptionEmail(%q) accepted an invalid address", input)
		}
	}
}

func TestUnsubscribeTokenIsStableAndSecretBound(t *testing.T) {
	uuid := "b5f2c914-af0d-4a00-baa2-93f35f0bcf11"
	token := buildUnsubscribeToken("shared-secret", uuid)
	if token != buildUnsubscribeToken("shared-secret", uuid) {
		t.Fatal("the unsubscribe token must be deterministic")
	}
	if token == buildUnsubscribeToken("another-secret", uuid) {
		t.Fatal("the token must depend on the secret")
	}
	got, ok := verifyUnsubscribeToken("shared-secret", token, utils.SHA256Hex(token))
	if !ok || got != uuid {
		t.Fatalf("a valid token was rejected: got %q, ok=%v", got, ok)
	}
	if _, ok := verifyUnsubscribeToken("another-secret", token, utils.SHA256Hex(token)); ok {
		t.Fatal("a token signed with another secret was accepted")
	}
	if _, ok := verifyUnsubscribeToken("shared-secret", token, "not-the-hash"); ok {
		t.Fatal("a token that does not match the stored hash was accepted")
	}
	if _, ok := verifyUnsubscribeToken("shared-secret", uuid, ""); ok {
		t.Fatal("a token without its signature was accepted")
	}
}

func TestIsDownStatus(t *testing.T) {
	for _, status := range []models.AggregateStatus{models.AggregateDown, models.AggregateDegraded} {
		if !isDownStatus(status) {
			t.Errorf("%s should open an incident", status)
		}
	}
	for _, status := range []models.AggregateStatus{models.AggregateUp, models.AggregatePending, models.AggregateUnknown, models.AggregateMaintenance} {
		if isDownStatus(status) {
			t.Errorf("%s should not open an incident", status)
		}
	}
}
