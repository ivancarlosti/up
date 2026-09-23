package scheduler

import "sort"

// reconcilePlan is the worker set change computed by planReconcile: the monitor
// ids that must start, the ones that must stop and the ones that keep running
// (their configuration is refreshed from the database).
type reconcilePlan struct {
	Start []uint
	Stop  []uint
	Keep  []uint
}

// changed reports whether the plan alters the running worker set.
func (p reconcilePlan) changed() bool { return len(p.Start) > 0 || len(p.Stop) > 0 }

// planReconcile compares the monitors this node should run with the workers that
// are already running and returns the difference.
//
// It is the pure core of the periodic reconciliation. A monitor created (or
// deleted) on ANOTHER node of the cluster only reaches this node through the
// shared database: nothing pushes it here, because the CRUD handlers can only
// touch the process that served the request (`handlers/monitors.go` calls
// `scheduleUpsert`/`scheduleRemove` locally) and the WebSocket hub is
// per-process. Without this the worker set of a node stayed frozen until the
// next restart.
func planReconcile(expectedIDs, runningIDs []uint) reconcilePlan {
	running := make(map[uint]struct{}, len(runningIDs))
	for _, id := range runningIDs {
		running[id] = struct{}{}
	}
	expected := make(map[uint]struct{}, len(expectedIDs))
	for _, id := range expectedIDs {
		expected[id] = struct{}{}
	}

	plan := reconcilePlan{}
	for _, id := range expectedIDs {
		if _, ok := running[id]; ok {
			plan.Keep = append(plan.Keep, id)
			continue
		}
		plan.Start = append(plan.Start, id)
	}
	for _, id := range runningIDs {
		if _, ok := expected[id]; !ok {
			plan.Stop = append(plan.Stop, id)
		}
	}

	// The callers build the id lists from maps, so the order is not stable:
	// sorting keeps the logs readable and the tests deterministic.
	sort.Slice(plan.Start, func(i, j int) bool { return plan.Start[i] < plan.Start[j] })
	sort.Slice(plan.Stop, func(i, j int) bool { return plan.Stop[i] < plan.Stop[j] })
	sort.Slice(plan.Keep, func(i, j int) bool { return plan.Keep[i] < plan.Keep[j] })
	return plan
}
