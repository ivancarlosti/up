package models

import (
	"strconv"
	"strings"
	"testing"
)

// TestEncodeDNSNames documents how the subject alternative names are stored: the
// column is a TEXT, a CDN certificate lists hundreds of names, and the
// observation must never be rejected because of its size (a monitor without a
// stored certificate also loses its expiration reminders).
func TestEncodeDNSNames(t *testing.T) {
	value, dropped := EncodeDNSNames([]string{"*.google.com", "google.com", "*.youtube.com", "youtu.be"})
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	if want := "*.google.com,google.com,*.youtube.com,youtu.be"; value != want {
		t.Fatalf("value = %q, want %q", value, want)
	}

	// Blank entries are skipped instead of producing an empty element.
	value, dropped = EncodeDNSNames([]string{"a.example.com", "   ", "b.example.com"})
	if dropped != 0 || value != "a.example.com,b.example.com" {
		t.Fatalf("value = %q dropped = %d", value, dropped)
	}

	nothing, dropped := EncodeDNSNames(nil)
	if nothing != "" || dropped != 0 {
		t.Fatalf("empty list -> %q dropped = %d", nothing, dropped)
	}
}

// TestEncodeDNSNamesBudget keeps a pathological list from overflowing the column
// again: the tail is dropped, the list stays well formed and no name is cut in
// half.
func TestEncodeDNSNamesBudget(t *testing.T) {
	huge := make([]string, 0, 5000)
	for index := 0; index < 5000; index++ {
		huge = append(huge, "very-long-subdomain-"+strconv.Itoa(index)+".example.com")
	}

	value, dropped := EncodeDNSNames(huge)
	if dropped == 0 {
		t.Fatal("a list larger than the budget must drop entries")
	}
	if len(value) > MaxCertDNSNames {
		t.Fatalf("value is %d bytes, over the %d budget", len(value), MaxCertDNSNames)
	}
	if strings.HasSuffix(value, ",") || strings.Contains(value, ",,") {
		t.Fatal("the stored list must stay well formed")
	}
	if !strings.HasSuffix(value, ".example.com") {
		t.Fatal("the last name must not be cut in half")
	}
	if kept := len(strings.Split(value, ",")); kept+dropped != len(huge) {
		t.Fatalf("kept %d + dropped %d != %d", kept, dropped, len(huge))
	}
}

// TestEncodeDNSNamesStoresARealCDNCertificate is the regression guard of the
// "Data too long for column 'dns_names'" incident: a certificate that lists a
// hundred subject alternative names (well past the old varchar(500)) is stored
// in full instead of losing the monitor its certificate.
func TestEncodeDNSNamesStoresARealCDNCertificate(t *testing.T) {
	names := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		names = append(names, "*.service-"+strconv.Itoa(index)+".example.com")
	}

	value, dropped := EncodeDNSNames(names)
	if dropped != 0 {
		t.Fatalf("dropped = %d, a realistic certificate must be stored in full", dropped)
	}
	if len(value) <= 500 {
		t.Fatalf("the fixture is %d bytes; it must exceed the old varchar(500) to guard the incident", len(value))
	}
	if kept := len(strings.Split(value, ",")); kept != len(names) {
		t.Fatalf("stored %d names, want %d", kept, len(names))
	}
}
