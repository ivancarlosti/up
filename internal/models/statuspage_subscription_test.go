package models

import "testing"

func TestStatusPageSubscriberUUIDIsDeterministicAndCaseInsensitive(t *testing.T) {
	pageUUID := "3f1b0f3e-6a5d-4c7b-9d11-2a9f0f0b1c22"
	first := StatusPageSubscriberUUID(pageUUID, "  Person@Example.COM ")
	again := StatusPageSubscriberUUID(pageUUID, "person@example.com")
	if first != again {
		t.Fatalf("the same address yielded two identities: %q and %q", first, again)
	}
	if first == StatusPageSubscriberUUID("another-page", "person@example.com") {
		t.Fatal("the page uuid must take part in the identity")
	}
	if first == StatusPageSubscriberUUID(pageUUID, "other@example.com") {
		t.Fatal("the address must take part in the identity")
	}
}

func TestNormalizeSubscriptionDelayMinutes(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultSubscriptionDelayMinutes},
		{-3, DefaultSubscriptionDelayMinutes},
		{1, MinSubscriptionDelayMinutes},
		{5, 5},
		{7, 5},
		{8, 10},
		{60, 60},
		{1441, MaxSubscriptionDelayMinutes},
		{99999, MaxSubscriptionDelayMinutes},
	}
	for _, tc := range cases {
		if got := NormalizeSubscriptionDelayMinutes(tc.in); got != tc.want {
			t.Errorf("NormalizeSubscriptionDelayMinutes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
