package services

import "testing"

// TestFirstGroupID pins the truncation that makes the single group rule hold on
// the deprecated `group_ids` contract: whatever a client sends, exactly one id is
// kept, and it is the FIRST usable one (a leading zero cannot win the place).
func TestFirstGroupID(t *testing.T) {
	cases := []struct {
		name string
		in   []uint
		want uint
	}{
		{name: "none", in: nil, want: 0},
		{name: "empty", in: []uint{}, want: 0},
		{name: "zeros only", in: []uint{0, 0}, want: 0},
		{name: "single", in: []uint{7}, want: 7},
		{name: "the first of many wins", in: []uint{3, 9, 4}, want: 3},
		{name: "a leading zero is skipped", in: []uint{0, 5, 6}, want: 5},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := firstGroupID(testCase.in); got != testCase.want {
				t.Errorf("firstGroupID(%v) = %d, want %d", testCase.in, got, testCase.want)
			}
		})
	}
}

// TestBulkGroupIDs pins how much the template decides for a bulk creation: nothing
// when the request names a group, its own single group when the request says
// nothing at all, and nothing when the request asks explicitly for no group.
func TestBulkGroupIDs(t *testing.T) {
	cases := []struct {
		name     string
		request  []uint
		template []uint
		want     []uint
	}{
		{name: "the request wins", request: []uint{4}, template: []uint{9}, want: []uint{4}},
		{name: "only the first of the request counts", request: []uint{4, 5}, template: []uint{9}, want: []uint{4}},
		{name: "silence falls back to the template", request: nil, template: []uint{9, 5}, want: []uint{9}},
		{name: "silence and an empty template means no group", request: nil, template: nil, want: []uint{}},
		{name: "an explicit empty list beats the template", request: []uint{}, template: []uint{9}, want: []uint{}},
		{name: "zeros in the request fall back to the template", request: []uint{0}, template: []uint{9}, want: []uint{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := bulkGroupIDs(testCase.request, testCase.template)
			if len(got) != len(testCase.want) {
				t.Fatalf("bulkGroupIDs(%v, %v) = %v, want %v",
					testCase.request, testCase.template, got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("bulkGroupIDs(%v, %v) = %v, want %v",
						testCase.request, testCase.template, got, testCase.want)
				}
			}
		})
	}
}
