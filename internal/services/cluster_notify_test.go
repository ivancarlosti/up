package services

import (
	"testing"
	"time"
)

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

// TestShouldResend pins the re-notification decision of an established incident: the
// interval is the larger of the monitor's and of the channel's, so a channel
// configured to repeat more often than its monitor does is honoured, and an interval
// of 0 (the default everywhere) means "transitions only".
func TestShouldResend(t *testing.T) {
	reported := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name           string
		monitorSeconds int
		channelSeconds int
		elapsed        time.Duration
		want           bool
	}{
		{
			name:    "no interval configured never repeats",
			elapsed: 24 * time.Hour, want: false,
		},
		{
			name:           "the monitor interval repeats once it is due",
			monitorSeconds: 3600, elapsed: time.Hour, want: true,
		},
		{
			name:           "the monitor interval waits when it is the larger one",
			monitorSeconds: 3600, channelSeconds: 60, elapsed: 30 * time.Minute, want: false,
		},
		{
			name:           "a channel can repeat more often than its monitor",
			channelSeconds: 60, elapsed: time.Minute, want: true,
		},
		{
			name:           "a channel can also stretch the cadence",
			monitorSeconds: 300, channelSeconds: 600, elapsed: 5 * time.Minute, want: false,
		},
		{
			name:           "the stretched cadence fires when it is due",
			monitorSeconds: 300, channelSeconds: 600, elapsed: 10 * time.Minute, want: true,
		},
		{
			name:           "the boundary is inclusive",
			monitorSeconds: 300, elapsed: 5 * time.Minute, want: true,
		},
		{
			name:           "a little short of the boundary is not enough",
			monitorSeconds: 300, elapsed: 5*time.Minute - time.Second, want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldResend(tc.monitorSeconds, tc.channelSeconds, reported, reported.Add(tc.elapsed))
			if got != tc.want {
				t.Errorf("shouldResend(%d, %d, +%s) = %t, want %t",
					tc.monitorSeconds, tc.channelSeconds, tc.elapsed, got, tc.want)
			}
		})
	}
}
