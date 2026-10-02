package services

import "testing"

// TestBulkGroupID pins how much the template decides for a bulk creation: nothing
// when the request names a group, its own single group when the request says
// nothing at all, and nothing when the request asks explicitly for no group.
//
// The three states of the pointer are the whole contract, and they mirror the
// monitor payload: null is "the request did not decide", 0 is "no group" and an id
// is the group the created monitors join.
func TestBulkGroupID(t *testing.T) {
	ptr := func(value uint) *uint { return &value }
	cases := []struct {
		name    string
		request *uint
		fromTpl uint
		want    uint
	}{
		{name: "nothing asked, no template group", request: nil, fromTpl: 0, want: 0},
		{name: "nothing asked, the template decides", request: nil, fromTpl: 9, want: 9},
		{name: "the request wins over the template", request: ptr(4), fromTpl: 9, want: 4},
		{name: "an explicit no-group beats the template", request: ptr(0), fromTpl: 9, want: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := bulkGroupID(testCase.request, testCase.fromTpl); got != testCase.want {
				t.Fatalf("bulkGroupID(%v, %d) = %d, want %d",
					testCase.request, testCase.fromTpl, got, testCase.want)
			}
		})
	}
}
