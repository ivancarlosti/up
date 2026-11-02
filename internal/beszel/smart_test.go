package beszel

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSmartReportJSONKeys pins the shape the browser reads.
//
// The report is stored exactly as it is marshalled here (models.SystemSmart.Disks)
// and served verbatim by the history, the S.M.A.R.T. page and the peer endpoint,
// while web/src/lib/types-systems.ts reads snake_case. A Go field without a json
// tag therefore does not fail anything on this side: the S.M.A.R.T. view simply
// renders the device name, no attributes and an "Unknown" badge, which is exactly
// what happened before the tags were added.
func TestSmartReportJSONKeys(t *testing.T) {
	report := Smart{
		ModelName:   "Samsung SSD 870 EVO 1TB",
		Status:      "PASSED",
		Capacity:    1000204886016,
		Temperature: 34,
		Attributes: []SmartAttr{
			{ID: 5, Name: "Reallocated_Sector_Ct", Value: 100, Worst: 100, Threshold: 10, RawValue: 0, RawString: "0"},
			{ID: 9, Name: "Power_On_Hours", Value: 98, Worst: 98, Threshold: 0, RawValue: 18005, RawString: "18005"},
		},
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(encoded)

	for _, key := range []string{
		`"model_name"`, `"status"`, `"capacity"`, `"temperature"`,
		`"attributes"`, `"name"`, `"raw_value"`, `"raw_string"`, `"threshold"`,
	} {
		if !strings.Contains(body, key) {
			t.Errorf("the report has no %s key: %s", key, body)
		}
	}
	// The Go field names would still be readable by the frontend typos, so the
	// test also refuses them instead of only checking that the new keys exist.
	for _, key := range []string{`"ModelName"`, `"RawString"`, `"Attributes"`} {
		if strings.Contains(body, key) {
			t.Errorf("the report still carries the Go field name %s: %s", key, body)
		}
	}

	// The map the stored JSON really holds is keyed by device name, which is what
	// both views iterate over.
	disks := map[string]Smart{"sda": report}
	encoded, err = json.Marshal(disks)
	if err != nil {
		t.Fatalf("Marshal(map): %v", err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := decoded["sda"]["model_name"]; !ok {
		t.Fatalf("the disk object lost its model_name: %s", encoded)
	}
}

// TestSmartDiskHealthy pins what the `smart` alert counts: only a positive bad
// verdict, never the silence of a disk the agent could not read.
func TestSmartDiskHealthy(t *testing.T) {
	cases := map[string]bool{
		"":          true,
		"  ":        true,
		"PASSED":    true,
		"passed":    true,
		" OK ":      true,
		"GOOD":      true,
		"FAILED!":   false,
		"failed":    false,
		"bad":       false,
		"Failing":   false,
		"UNKNOWN!":  false,
		"Critical":  false,
		"Attention": false,
	}
	for status, want := range cases {
		if got := SmartDiskHealthy(status); got != want {
			t.Errorf("SmartDiskHealthy(%q) = %v, want %v", status, got, want)
		}
	}
}
