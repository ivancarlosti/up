package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestHeartbeatBucketStatus pins the rule that reduces a bucket to one colour: a
// single failed check marks the slot as down even when the bucket also saw
// successes, an empty bucket has no status at all (the UI draws "no data"), and
// the ranking is down > pending > maintenance > up.
func TestHeartbeatBucketStatus(t *testing.T) {
	cases := []struct {
		name string
		row  heartbeatBucketRow
		want string
	}{
		{"empty bucket", heartbeatBucketRow{}, ""},
		{"all up", heartbeatBucketRow{Total: 3}, models.StatusUp.String()},
		{"one down wins", heartbeatBucketRow{Total: 3, Downs: 1}, models.StatusDown.String()},
		{"down beats pending", heartbeatBucketRow{Total: 3, Downs: 1, Pendings: 1}, models.StatusDown.String()},
		{"pending before maintenance", heartbeatBucketRow{Total: 3, Pendings: 1, Maintenances: 1}, models.StatusPending.String()},
		{"maintenance only", heartbeatBucketRow{Total: 1, Maintenances: 1}, models.StatusMaintenance.String()},
	}
	for _, tc := range cases {
		if got := tc.row.status(); got != tc.want {
			t.Errorf("%s: status() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
