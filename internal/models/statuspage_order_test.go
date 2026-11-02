package models

import "testing"

// TestNormalizeMonitorOrder pins the value every path that is not the admin form ends
// up with: an empty or unknown order must render the page in the order the operator
// set, never fail and never pick a mode nobody asked for.
func TestNormalizeMonitorOrder(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", MonitorOrderManual},
		{"manual", MonitorOrderManual},
		{"alphabetical", MonitorOrderAlphabetical},
		{"type", MonitorOrderType},
		{"Type", MonitorOrderManual},
		{"alphabetical ", MonitorOrderManual},
		{"uptime", MonitorOrderManual},
	}
	for _, tc := range cases {
		if got := NormalizeMonitorOrder(tc.in); got != tc.want {
			t.Errorf("NormalizeMonitorOrder(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAllMonitorOrders proves the list the API and the dialog offer is exactly what
// NormalizeMonitorOrder accepts: a mode added to one and not the other would be
// silently reset to manual on save.
func TestAllMonitorOrders(t *testing.T) {
	orders := AllMonitorOrders()
	if len(orders) != 3 {
		t.Fatalf("expected 3 orders, got %v", orders)
	}
	if orders[0] != MonitorOrderManual {
		t.Fatalf("manual must be first (it is the default), got %v", orders)
	}
	for _, order := range orders {
		if NormalizeMonitorOrder(order) != order {
			t.Errorf("%q is offered but not accepted by NormalizeMonitorOrder", order)
		}
	}
}

// TestMonitorTypeRank pins the coarse order of the "by type" mode: AllMonitorTypes is
// the order the page shows, and an unknown type sinks below every known one.
func TestMonitorTypeRank(t *testing.T) {
	types := AllMonitorTypes()
	for index, monitorType := range types {
		if got := MonitorTypeRank(monitorType); got != index {
			t.Errorf("MonitorTypeRank(%q) = %d, want %d", monitorType, got, index)
		}
	}
	if got, want := MonitorTypeRank(MonitorType("smtp")), len(types); got != want {
		t.Errorf("an unknown type must sink to the end: got %d want %d", got, want)
	}
	// The rank of http is below keyword, which is below tcp: the list is the order the
	// page shows, not the alphabetical one.
	if MonitorTypeRank(MonitorTypeHTTP) >= MonitorTypeRank(MonitorTypeTCP) {
		t.Fatal("the type order must follow AllMonitorTypes")
	}
}
