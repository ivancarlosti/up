package models

import "testing"

// TestNormalizeTags pins the canonical form of a tag list: trimmed, de-duplicated
// case insensitively, order preserved. It is the form the write paths store, so
// "prod, prod" must not survive a save and a tag must not keep a stray space.
func TestNormalizeTags(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: []string{}},
		{name: "spaces only", raw: "  ,  ", want: []string{}},
		{name: "comma separated", raw: "prod, staging", want: []string{"prod", "staging"}},
		{name: "semicolons and tabs", raw: "prod;\tstaging", want: []string{"prod", "staging"}},
		{name: "order is preserved", raw: "b, a, c", want: []string{"b", "a", "c"}},
		{name: "duplicates collapse, first spelling wins", raw: "Ops, ops, OPS", want: []string{"Ops"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeTags(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("NormalizeTags(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("NormalizeTags(%q) = %v, want %v", tc.raw, got, tc.want)
				}
			}
		})
	}
}

// TestJoinTagsAndCleanTags pins the stored string and, with it, the promise the
// bulk tag path relies on: an edit that changes nothing produces the same string,
// so the row is not rewritten for nothing.
func TestJoinTagsAndCleanTags(t *testing.T) {
	if got := CleanTags("  prod ,, prod ,staging "); got != "prod,staging" {
		t.Fatalf("CleanTags = %q, want %q", got, "prod,staging")
	}
	if got := CleanTags("prod,staging"); got != "prod,staging" {
		t.Fatalf("a canonical list must be stable: %q", got)
	}
	if got := JoinTags([]string{"b", "a"}); got != "b,a" {
		t.Fatalf("JoinTags must not reorder: %q", got)
	}
	if got := CleanTags(""); got != "" {
		t.Fatalf("CleanTags(\"\") = %q, want empty", got)
	}
}

// TestAddAndRemoveTags covers the two halves of a bulk tag edit, including the
// rename they make together.
func TestAddAndRemoveTags(t *testing.T) {
	if got := AddTags("prod", []string{"staging"}); got != "prod,staging" {
		t.Fatalf("AddTags = %q", got)
	}
	// Adding a tag that is already there (in another case) keeps the stored
	// spelling and does not duplicate it.
	if got := AddTags("prod", []string{"PROD"}); got != "prod" {
		t.Fatalf("AddTags duplicated a tag: %q", got)
	}
	if got := RemoveTags("prod,staging", []string{"PROD"}); got != "staging" {
		t.Fatalf("RemoveTags = %q", got)
	}
	if got := RemoveTags("prod", []string{"staging"}); got != "prod" {
		t.Fatalf("RemoveTags dropped an unrelated tag: %q", got)
	}
	// A rename is a remove plus an add, in one pass.
	renamed := AddTags(RemoveTags("prod,staging", []string{"prod"}), []string{"production"})
	if renamed != "staging,production" {
		t.Fatalf("rename = %q", renamed)
	}
}

// TestHasAnyTag pins the rule the tag scope and the bulk tag selection share:
// whole tags only, case insensitively, and an empty selection never matches.
func TestHasAnyTag(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		wanted []string
		want   bool
	}{
		{name: "whole tag", stored: "prod,staging", wanted: []string{"prod"}, want: true},
		{name: "case insensitive", stored: "Prod", wanted: []string{"prod"}, want: true},
		{name: "substring is not a match", stored: "production", wanted: []string{"prod"}, want: false},
		{name: "prefix is not a match", stored: "zz-tag", wanted: []string{"zz"}, want: false},
		{name: "any of the wanted", stored: "prod", wanted: []string{"staging", "prod"}, want: true},
		{name: "none of the wanted", stored: "prod", wanted: []string{"staging"}, want: false},
		{name: "empty selection never matches", stored: "prod", wanted: nil, want: false},
		{name: "no tags", stored: "", wanted: []string{"prod"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasAnyTag(tc.stored, tc.wanted); got != tc.want {
				t.Fatalf("HasAnyTag(%q, %v) = %v, want %v", tc.stored, tc.wanted, got, tc.want)
			}
		})
	}
}
