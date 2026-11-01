package services

import (
	"testing"
	"time"

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

// TestOutageMailStillOwed covers the cut-off that decides whether a queued outage
// mail survives the closure of its incident: the delay is a floor, so a mail that
// was due before the recovery still goes out (followed by the recovery), while one
// that was not due yet is cancelled.
func TestOutageMailStillOwed(t *testing.T) {
	scheduled := time.Date(2026, 5, 10, 10, 10, 0, 0, time.UTC)

	if !outageMailStillOwed(nil, scheduled) {
		t.Error("an open incident still owes its outage mail")
	}

	beforeDue := scheduled.Add(-2 * time.Minute)
	if outageMailStillOwed(&beforeDue, scheduled) {
		t.Error("an outage mail that was not due when the incident closed must be cancelled")
	}

	atDue := scheduled
	if !outageMailStillOwed(&atDue, scheduled) {
		t.Error("an outage mail due at the very moment the incident closed is still owed")
	}

	afterDue := scheduled.Add(2 * time.Minute)
	if !outageMailStillOwed(&afterDue, scheduled) {
		t.Error("an outage mail due before the incident closed is still owed")
	}
}

// TestRecoveryMailAction covers the gate of the recovery mail: it may only leave
// after the outage mail of the same incident, never before and never instead.
func TestRecoveryMailAction(t *testing.T) {
	cases := []struct {
		status string
		want   recoveryMailAction
	}{
		{models.SubscriptionNoticeSent, recoveryDeliver},
		{models.SubscriptionNoticePending, recoveryWaitForOutage},
		{models.SubscriptionNoticeSending, recoveryWaitForOutage},
		{models.SubscriptionNoticeSkipped, recoveryAbandon},
		{models.SubscriptionNoticeFailed, recoveryAbandon},
		{"", recoveryAbandon},
	}
	for _, test := range cases {
		if got := recoveryMailActionFor(test.status); got != test.want {
			t.Errorf("recoveryMailActionFor(%q) = %d, want %d", test.status, got, test.want)
		}
	}
}
