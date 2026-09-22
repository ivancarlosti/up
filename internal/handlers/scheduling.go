package handlers

// The scheduler is optional from the point of view of the HTTP layer: it is
// always injected in main, but the small wrappers below keep the handlers safe
// (and testable) when it is not.

func (h *Container) scheduleUpsert(monitorID uint) {
	if h.Scheduler != nil {
		h.Scheduler.Upsert(monitorID)
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
