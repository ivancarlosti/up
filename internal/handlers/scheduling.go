package handlers

// The scheduler is optional from the point of view of the HTTP layer: it is
// always injected in main, but the small wrappers below keep the handlers safe
// (and testable) when it is not.

func (h *Container) scheduleUpsert(monitorID uint) {
	if h.Scheduler != nil {
		h.Scheduler.Upsert(monitorID)
	}
}

// bulkScheduleThreshold is the batch size above which the created monitors are
// reconciled with one scheduler reload instead of one upsert per monitor.
//
// The scheduler command channel buffers 64 commands and drops what does not fit
// (the periodic reconciliation is the safety net), so enqueueing one command per
// row of a 2k import leaves most of the new monitors unscheduled until the next
// pass. A single reload walks the database once and configures them all.
const bulkScheduleThreshold = 64

// scheduleUpserts configures a freshly created batch: one upsert per monitor
// while they fit in the scheduler buffer, a single reload when they do not.
func (h *Container) scheduleUpserts(monitorIDs []uint) {
	if h.Scheduler == nil {
		return
	}
	if len(monitorIDs) > bulkScheduleThreshold {
		h.Scheduler.Reload()
		return
	}
	for _, id := range monitorIDs {
		h.Scheduler.Upsert(id)
	}
}

func (h *Container) scheduleRemove(monitorID uint) {
	if h.Scheduler != nil {
		h.Scheduler.Remove(monitorID)
	}
}

func (h *Container) scheduleCheckNow(monitorID uint) {
	if h.Scheduler != nil {
		h.Scheduler.CheckNow(monitorID)
	}
}

func (h *Container) scheduleReload() {
	if h.Scheduler != nil {
		h.Scheduler.Reload()
	}
}
