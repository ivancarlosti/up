package models

import "testing"

// TestNormalizeIPFamily documents the canonicalization: an empty value (a monitor
// written before the field existed, or a payload that omits it) means the default
// preference (auto) and the accepted spellings of the families are understood,
// while an unreadable value is only trimmed and lowered so Validate can reject it
// (a typo must not silently become the default).
func TestNormalizeIPFamily(t *testing.T) {
	cases := map[string]string{
		"":          IPFamilyAuto,
		"  ":        IPFamilyAuto,
		"alternate": IPFamilyAlternate,
		"ALTERNATE": IPFamilyAlternate,
		"nope":      "nope",
		" ipv7 ":    "ipv7",
		"auto":      IPFamilyAuto,
		"AUTO":      IPFamilyAuto,
		"ipv4":      IPFamily4,
		"IPv4":      IPFamily4,
		"4":         IPFamily4,
		"v4":        IPFamily4,
		"tcp4":      IPFamily4,
		"ipv6":      IPFamily6,
		"IPv6":      IPFamily6,
		"6":         IPFamily6,
		" tcp6 ":    IPFamily6,
	}
	for input, want := range cases {
		if got := NormalizeIPFamily(input); got != want {
			t.Errorf("NormalizeIPFamily(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestValidIPFamily pins the contract the API relies on: an omitted field is
// valid (the endpoint cannot break a client that predates it) and anything else
// must be one of the four documented values. The aliases ("4", "tcp6", ...) are a
// courtesy of NormalizeIPFamily, which runs first on the write path; the typo it
// does not know reaches this check unchanged and is rejected with 400.
func TestValidIPFamily(t *testing.T) {
	for _, valid := range append([]string{""}, IPFamilies...) {
		if !ValidIPFamily(valid) {
			t.Errorf("ValidIPFamily(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"ipv7", "alternate-", "both", "nope", "tcp"} {
		if ValidIPFamily(invalid) {
			t.Errorf("ValidIPFamily(%q) = true, want false", invalid)
		}
		if normalized := NormalizeIPFamily(invalid); ValidIPFamily(normalized) {
			t.Errorf("NormalizeIPFamily(%q) = %q, which must still be invalid", invalid, normalized)
		}
	}
	// The aliases are normalized, never validated as written: they never reach
	// the check, which is what makes "tcp6" a working value for a hand written
	// payload without widening the stored vocabulary.
	for alias, canonical := range map[string]string{"4": IPFamily4, "tcp4": IPFamily4, "6": IPFamily6, "tcp6": IPFamily6, "AUTO": IPFamilyAuto} {
		if got := NormalizeIPFamily(alias); got != canonical {
			t.Errorf("NormalizeIPFamily(%q) = %q, want %q", alias, got, canonical)
		}
	}
}

// TestNextIPFamily covers the rotation itself: it always flips, and the first
// (empty) turn is IPv4, so two consecutive executions always test both paths.
func TestNextIPFamily(t *testing.T) {
	if got := NextIPFamily(""); got != IPFamily4 {
		t.Fatalf("NextIPFamily(\"\") = %q, want %q", got, IPFamily4)
	}
	if got := NextIPFamily(IPFamily4); got != IPFamily6 {
		t.Fatalf("NextIPFamily(ipv4) = %q, want %q", got, IPFamily6)
	}
	if got := NextIPFamily(IPFamily6); got != IPFamily4 {
		t.Fatalf("NextIPFamily(ipv6) = %q, want %q", got, IPFamily4)
	}
	// A nonsense value is treated as the default state, so the rotation still
	// advances instead of stalling on an unreadable stored value.
	if got := NextIPFamily("nope"); got != IPFamily4 {
		t.Fatalf("NextIPFamily(nope) = %q, want %q", got, IPFamily4)
	}
}

// TestNetworkFor maps a preference to the network suffix the checkers append to
// "tcp"/"udp": only the two explicit families name one.
func TestNetworkFor(t *testing.T) {
	cases := map[string]string{
		IPFamily4:         "4",
		IPFamily6:         "6",
		IPFamilyAuto:      "",
		IPFamilyAlternate: "",
		"":                "",
	}
	for input, want := range cases {
		if got := NetworkFor(input); got != want {
			t.Errorf("NetworkFor(%q) = %q, want %q", input, got, want)
		}
	}
}
