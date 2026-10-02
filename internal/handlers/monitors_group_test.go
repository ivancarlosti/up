package handlers

import (
	"encoding/json"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
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

// TestMonitorPayloadGroupKeys pins the group key of the monitor payload.
//
// The embedded models.Monitor carries a runtime `group_id` too, so the pointer
// field of monitorPayload has to SHADOW it: without the shadowing a request would
// fill the model field, which the write path never reads, and every group would
// look "not mentioned" (an update would then keep the group the monitor already
// has, silently ignoring the operator).
//
// The three states the pointer has to keep apart are the whole contract:
//   - the key is absent  -> the current group is kept (nil)
//   - the key is 0       -> the monitor moves to no group
//   - the key is an id   -> the monitor joins that group
func TestMonitorPayloadGroupKeys(t *testing.T) {
	omitted := bindMonitorPayload(t, `{"name":"api"}`)
	if omitted.GroupID != nil {
		t.Fatalf("an omitted group_id must stay nil (keep the current group), got %d", *omitted.GroupID)
	}
	if omitted.Monitor.GroupID != 0 {
		t.Fatalf("the embedded model field must stay untouched, got %d", omitted.Monitor.GroupID)
	}

	cleared := bindMonitorPayload(t, `{"name":"api","group_id":0}`)
	if cleared.GroupID == nil || *cleared.GroupID != 0 {
		t.Fatalf("group_id=0 must decode to a zero pointer (no group), got %#v", cleared.GroupID)
	}

	joined := bindMonitorPayload(t, `{"name":"api","group_id":5}`)
	if joined.GroupID == nil || *joined.GroupID != 5 {
		t.Fatalf("group_id was not decoded into the payload pointer: %#v", joined.GroupID)
	}
	if joined.Monitor.GroupID != 0 {
		t.Fatalf("the embedded model field must stay untouched, got %d", joined.Monitor.GroupID)
	}
}

// TestMonitorJSONExposesTheFlatGroupOnly pins the shape of the group on the read
// path: the payload carries the pair flat (`group_id` / `group_name`) and no
// nested `group` object. The nested form only duplicated the pair (same id, same
// name, no reader), so a future field that reintroduces it would make the two
// representations drift apart again.
func TestMonitorJSONExposesTheFlatGroupOnly(t *testing.T) {
	raw, err := json.Marshal(models.Monitor{GroupID: 5, GroupName: "Prod"})
	if err != nil {
		t.Fatalf("marshalling the monitor: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	if got := decoded["group_id"]; got != float64(5) {
		t.Errorf("group_id = %#v, want 5", got)
	}
	if got := decoded["group_name"]; got != "Prod" {
		t.Errorf("group_name = %#v, want %q", got, "Prod")
	}
	if _, ok := decoded["group"]; ok {
		t.Errorf("a monitor must not carry a nested group object any more: %s", raw)
	}
}
