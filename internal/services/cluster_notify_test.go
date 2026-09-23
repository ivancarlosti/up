package services

import "testing"

// stateColumnIn reports whether a column list contains name.
func stateColumnIn(columns []string, name string) bool {
	for _, column := range columns {
		if column == name {
			return true
		}
	}
	return false
}

// TestStateUpdateColumns pins the invariant behind the resend clock: only the
// node that actually dispatched the notification may write notified /
// notified_at back into monitor_states.
//
// Before this was separated, the node that lost the claim wrote back the values
// it had read before the winner committed, rewinding notified_at. With
// resend_interval_seconds > 0 that repeats the same alert earlier than
// configured.
func TestStateUpdateColumns(t *testing.T) {
	cases := []struct {
		name       string
		dispatched bool
		want       bool
	}{
		{name: "the sender advances the notification bookkeeping", dispatched: true, want: true},
		{name: "a node that did not send leaves it untouched", dispatched: false, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			columns := stateUpdateColumns(tc.dispatched)
			for _, bookkeeping := range []string{"notified", "notified_at"} {
				if got := stateColumnIn(columns, bookkeeping); got != tc.want {
					t.Errorf("%s present = %t, want %t", bookkeeping, got, tc.want)
				}
			}
			// The aggregated status is refreshed by every node, dispatching or
			// not: a node that lost the claim must still keep it current.
			for _, column := range []string{
				"status", "changed_at", "last_message", "last_latency", "last_check_at", "updated_at",
			} {
				if !stateColumnIn(columns, column) {
					t.Errorf("column %q must always be refreshed", column)
				}
			}
		})
	}
}
