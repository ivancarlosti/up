package handlers

import (
	"encoding/json"
	"testing"
)

// bindMonitorPayload decodes a create/update body the way bindJSON does.
func bindMonitorPayload(t *testing.T, body string) monitorPayload {
	t.Helper()
	var payload monitorPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return payload
}

// TestMonitorPayloadGroupKeys pins the two group keys of the monitor payload.
//
// The embedded models.Monitor carries a runtime `group_id` too, so the pointer
// fields of monitorPayload have to SHADOW it: without the shadowing a request would
// fill the model field, which the write path never reads, and every group would look
// "not mentioned" (an update would then keep the group the monitor already has,
// silently ignoring the operator).
func TestMonitorPayloadGroupKeys(t *testing.T) {
	singular := bindMonitorPayload(t, `{"name":"api","group_id":5}`)
	if singular.GroupID == nil || *singular.GroupID != 5 {
		t.Fatalf("group_id was not decoded into the payload pointer: %#v", singular.GroupID)
	}
	if singular.Monitor.GroupID != 0 {
		t.Fatalf("the embedded model field must stay untouched, got %d", singular.Monitor.GroupID)
	}
	if got := monitorGroupSelection(singular.GroupID, singular.GroupIDs); len(got) != 1 || got[0] != 5 {
		t.Fatalf("group_id=5 selected %v, want [5]", got)
	}

	list := bindMonitorPayload(t, `{"name":"api","group_ids":[3,4]}`)
	if list.GroupIDs == nil {
		t.Fatal("group_ids was not decoded into the payload pointer")
	}
	if got := monitorGroupSelection(list.GroupID, list.GroupIDs); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("group_ids=[3,4] selected %v, want the list handed to the service", got)
	}
}

// TestMonitorGroupSelection pins the contract the create/update handlers rely on:
// the singular key wins, an omitted field keeps the current group and an explicit
// zero (or an empty list) clears it.
func TestMonitorGroupSelection(t *testing.T) {
	uintPtr := func(value uint) *uint { return &value }
	listPtr := func(values ...uint) *[]uint { return &values }
	zero := uint(0)

	cases := []struct {
		name     string
		groupID  *uint
		groupIDs *[]uint
		wantNil  bool
		want     []uint
	}{
		{name: "nothing mentioned", groupID: nil, groupIDs: nil, wantNil: true},
		{name: "the singular key wins", groupID: uintPtr(7), groupIDs: listPtr(1, 2), want: []uint{7}},
		{name: "zero clears the group", groupID: &zero, groupIDs: listPtr(1, 2), want: []uint{}},
		{name: "the list is accepted", groupID: nil, groupIDs: listPtr(2, 3), want: []uint{2, 3}},
		{name: "an empty list clears the group", groupID: nil, groupIDs: &([]uint{}), want: []uint{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := monitorGroupSelection(testCase.groupID, testCase.groupIDs)
			if testCase.wantNil {
				if got != nil {
					t.Fatalf("monitorGroupSelection() = %v, want nil (the current group is kept)", got)
				}
				return
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("monitorGroupSelection() = %v, want %v", got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("monitorGroupSelection() = %v, want %v", got, testCase.want)
				}
			}
		})
	}
}
