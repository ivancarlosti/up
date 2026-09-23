package services

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/ivancarlosti/up/internal/i18n"
)

// TestErrInternalKeepsNestedAPIError documents why a validation error raised
// inside a transaction is not flattened into a 500: the operator must receive
// the field level code and message (the group_ids validation is the case that
// exposed it).
func TestErrInternalKeepsNestedAPIError(t *testing.T) {
	nested := fmt.Errorf("creating monitor: %w",
		ErrBadRequest(i18n.CodeMonitorGroupInvalid, "group_ids contains an unknown group"))

	got := ErrInternal(nested)
	if got.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", got.Status, http.StatusBadRequest)
	}
	if got.Code != i18n.CodeMonitorGroupInvalid {
		t.Fatalf("code = %q, want %q", got.Code, i18n.CodeMonitorGroupInvalid)
	}
	if got.Message != "group_ids contains an unknown group" {
		t.Fatalf("message = %q", got.Message)
	}

	plain := ErrInternal(errors.New("dial tcp: connection refused"))
	if plain.Status != http.StatusInternalServerError || plain.Code != i18n.CodeInternal {
		t.Fatalf("a plain error must stay a 500 ERR_INTERNAL, got %d %s", plain.Status, plain.Code)
	}
}

// TestUniqueIDs keeps the membership payload clean: the zeroes and the repeated
// ids of an operator payload must not create duplicate rows.
func TestUniqueIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []uint
		want []uint
	}{
		{name: "empty", in: nil, want: []uint{}},
		{name: "zeros", in: []uint{0, 0}, want: []uint{}},
		{name: "duplicates", in: []uint{3, 1, 3, 1}, want: []uint{3, 1}},
		{name: "order is kept", in: []uint{9, 0, 4, 4}, want: []uint{9, 4}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := uniqueIDs(testCase.in)
			if len(got) != len(testCase.want) {
				t.Fatalf("uniqueIDs(%v) = %v, want %v", testCase.in, got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("uniqueIDs(%v) = %v, want %v", testCase.in, got, testCase.want)
				}
			}
		})
	}
}
