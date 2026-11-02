package models

import (
	"reflect"
	"testing"
)

// TestContainerRowScansTheJoinedColumns guards the two columns AllContainers
// selects from the join (services/system_stats.go).
//
// A `gorm:"-"` tag on these fields does not fail anything loudly: GORM simply
// skips the column while scanning, the API answers `"system_name": ""` for every
// row, and the "All containers" page shows an empty Session column. That is
// exactly what shipped before this test.
func TestContainerRowScansTheJoinedColumns(t *testing.T) {
	kind := reflect.TypeOf(ContainerRow{})
	for _, name := range []string{"SystemName", "SystemUUID"} {
		field, ok := kind.FieldByName(name)
		if !ok {
			t.Fatalf("ContainerRow lost its %s field", name)
		}
		if tag := field.Tag.Get("gorm"); tag != "" {
			t.Errorf("%s carries gorm:%q: the SELECT of AllContainers cannot fill it", name, tag)
		}
		if tag := field.Tag.Get("json"); tag == "" || tag == "-" {
			t.Errorf("%s is not serialised, so the browser cannot read it", name)
		}
	}
}
