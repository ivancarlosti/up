package main

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/boot"
	"github.com/ivancarlosti/up/internal/i18n"
)

// TestNextBackoffDoublesAndCaps pins the cadence the boot screen advertises: the
// wait doubles, and it never grows past bootRetryMax however long a cause takes
// to be fixed. The sequence starts at bootRetryInitial, which is the wait after
// the first failure.
func TestNextBackoffDoublesAndCaps(t *testing.T) {
	want := []time.Duration{
		30 * time.Second, // 15s -> 30s
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		bootRetryMax, // 4m doubled is 8m: capped, and it stays there
		bootRetryMax,
	}

	current := bootRetryInitial
	for i, expected := range want {
		current = nextBackoff(current)
		if current != expected {
			t.Fatalf("wait %d = %s, want %s", i+1, current, expected)
		}
	}
}

// TestFailKeepsTheClassifiedCode is the contract the retry loop depends on: a
// step that fails answers with its classified code (or, when the classifier has
// nothing to say, with the fallback of the step), and the boot loop reads it
// back with errors.As in recordRetry.
func TestFailKeepsTheClassifiedCode(t *testing.T) {
	// A dial failure: classified as unreachable, whatever the fallback says.
	cause := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	err := fail(i18n.CodeDatabaseMigration, cause, "migrate")

	var failure *bootFailure
	if !errors.As(err, &failure) {
		t.Fatalf("fail must return a *bootFailure, got %T", err)
	}
	if failure.code != i18n.CodeDatabaseUnreachable {
		t.Fatalf("code = %q, want %q", failure.code, i18n.CodeDatabaseUnreachable)
	}
	if !errors.Is(err, cause) {
		t.Fatal("the original error must stay reachable through errors.Is")
	}
	if !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("the message must name the step: %q", err.Error())
	}

	// An error the classifier does not know keeps the fallback code of the step.
	other := fail(i18n.CodeBootFailed, errors.New("the issuer answered 502"), "wiring the services")
	if !errors.As(other, &failure) {
		t.Fatalf("fail must return a *bootFailure, got %T", other)
	}
	if failure.code != i18n.CodeBootFailed {
		t.Fatalf("code = %q, want %q", failure.code, i18n.CodeBootFailed)
	}
	// detail is the raw driver message: the step name travels in Error() instead,
	// so the page never shows "wiring the services:" twice.
	if failure.detail != "the issuer answered 502" {
		t.Fatalf("detail = %q, want the raw error message", failure.detail)
	}
	if !strings.Contains(other.Error(), "wiring the services") {
		t.Fatalf("the message must name the step: %q", other.Error())
	}
}

// TestRecordRetryPublishesTheAttemptingFailure checks what the boot screen reads
// while the loop waits: the classified failure, the attempt counter and the
// backoff that follows - a failure being worked on, never a parked one.
func TestRecordRetryPublishesTheAttemptingFailure(t *testing.T) {
	state := boot.New("up-node-1")
	err := fail(i18n.CodeBootFailed, errors.New("the issuer answered 502"), "wiring the services")

	// fail no longer touches the state: until the loop records the attempt, the
	// state says nothing failed.
	if snap := state.Snapshot(); snap.Code != "" {
		t.Fatalf("the step must not record the failure itself: %+v", snap)
	}

	if code := recordRetry(state, 3, err, 2*time.Minute); code != i18n.CodeBootFailed {
		t.Fatalf("code = %q, want %q", code, i18n.CodeBootFailed)
	}
	snap := state.Snapshot()
	if snap.Status != boot.StatusDegraded || snap.Ready {
		t.Fatalf("a retrying step must be degraded and not ready: %+v", snap)
	}
	if snap.Attempts != 3 || snap.NextRetryMS != 120_000 {
		t.Fatalf("the counters must reach the client: %+v", snap)
	}
	if snap.Code != i18n.CodeBootFailed {
		t.Fatalf("code = %q, want %q", snap.Code, i18n.CodeBootFailed)
	}

	// A bare error (no *bootFailure to read) still degrades the state with a
	// code instead of leaving a blank reason on the page.
	if code := recordRetry(state, 1, errors.New("boom"), bootRetryInitial); code != i18n.CodeBootFailed {
		t.Fatalf("code = %q, want %q", code, i18n.CodeBootFailed)
	}
	if snap := state.Snapshot(); snap.Code != i18n.CodeBootFailed || snap.Attempts != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}
