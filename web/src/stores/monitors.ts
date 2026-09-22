import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/lib/api'
import { RealtimeClient } from '@/lib/ws'
import type { RealtimeEvent, RealtimeReason, RealtimeState } from '@/lib/ws'
import type { AggregateStatus, DashboardResponse, HeartbeatStatus, Monitor } from '@/lib/types'

type Summary = DashboardResponse['summary']

/** How often the fallback refreshes the dashboard without real time updates. */
const POLL_INTERVAL = 20000

/**
 * useMonitorStore keeps the monitor list, the dashboard counters and the live
 * connection. Heartbeats arriving through the WebSocket are applied in place,
 * which avoids re-fetching the whole dashboard on every check.
 *
 * When the real time channel is unavailable (a proxy that does not forward the
 * upgrade, a disabled hub, an expired session) the store keeps the dashboard
 * fresh by polling instead, so the UI never looks frozen.
 */
export const useMonitorStore = defineStore('monitors', () => {
  const monitors = ref<Monitor[]>([])
  const summary = ref<Summary | null>(null)
  const loading = ref(false)
  const state = ref<RealtimeState>('idle')
  const reason = ref<RealtimeReason>('')
  const polling = ref(false)
  const lastError = ref<string | null>(null)

  let client: RealtimeClient | null = null
  let pollTimer: ReturnType<typeof setInterval> | undefined

  const connected = computed(() => state.value === 'open')
  const downCount = computed(() => monitors.value.filter((m) => m.status === 'down').length)
  const sorted = computed(() => [...monitors.value].sort((a, b) => a.name.localeCompare(b.name)))

  /** load fetches the dashboard (list + counters); silent skips the spinner. */
  async function load(filters: Record<string, string> = {}, options: { silent?: boolean } = {}): Promise<void> {
    if (!options.silent) loading.value = true
    lastError.value = null
    try {
      const response = await api.dashboard(filters)
      monitors.value = response.monitors ?? []
      summary.value = response.summary
    } catch (error) {
      lastError.value = error instanceof Error ? error.message : String(error)
      if (!options.silent) throw error
    } finally {
      if (!options.silent) loading.value = false
    }
  }

  /** connect opens the live channel (idempotent). */
  function connect(): void {
    if (client) {
      client.retry()
      return
    }
    client = new RealtimeClient(['heartbeat', 'monitor.', 'notification.log', 'cluster.'])
    client.onState((value, why) => {
      state.value = value
      reason.value = why
      // A channel that gave up is what the polling fallback is for; a retry
      // through the store is what brings real time updates back.
      if (value === 'unavailable') startPolling()
      else stopPolling()
    })
    client.on('heartbeat', (event) => applyHeartbeat(event))
    client.on('monitor.status', (event) => applyStatus(event))
    client.connect()
  }

  /** disconnect closes the live channel. */
  function disconnect(): void {
    client?.close()
    client = null
    state.value = 'idle'
    reason.value = ''
    stopPolling()
  }

  /** retryRealtime asks the channel to try again after it gave up. */
  function retryRealtime(): void {
    client?.retry()
  }

  function startPolling(): void {
    if (pollTimer) return
    polling.value = true
    pollTimer = setInterval(() => {
      void load({}, { silent: true })
    }, POLL_INTERVAL)
  }

  function stopPolling(): void {
    if (!pollTimer) return
    clearInterval(pollTimer)
    pollTimer = undefined
    polling.value = false
  }

  /** upsert replaces or inserts a monitor in the list. */
  function upsert(monitor: Monitor): void {
    const index = monitors.value.findIndex((item) => item.id === monitor.id)
    if (index >= 0) monitors.value.splice(index, 1, { ...monitors.value[index], ...monitor })
    else monitors.value.push(monitor)
  }

  /** remove drops a monitor from the list. */
  function remove(monitorId: number): void {
    monitors.value = monitors.value.filter((item) => item.id !== monitorId)
  }

  function applyHeartbeat(event: RealtimeEvent): void {
    const payload = event.payload as
      | { monitor_id: number; status: HeartbeatStatus; latency_ms: number; created_at: string; node_id: string }
      | undefined
    if (!payload) return
    const monitor = monitors.value.find((item) => item.id === payload.monitor_id)
    if (!monitor) return

    monitor.last_latency_ms = payload.latency_ms
    monitor.last_check_at = payload.created_at
    const series = monitor.heartbeats ?? (monitor.heartbeats = [])
    series.push({
      status: payload.status,
      latency_ms: payload.latency_ms,
      created_at: payload.created_at,
      node_id: payload.node_id,
    })
    if (series.length > 60) series.splice(0, series.length - 60)
  }

  function applyStatus(event: RealtimeEvent): void {
    const payload = event.payload as { monitor_id: number; status: AggregateStatus } | undefined
    if (!payload) return
    const monitor = monitors.value.find((item) => item.id === payload.monitor_id)
    if (monitor) monitor.status = payload.status
    if (summary.value) refreshSummary()
  }

  function refreshSummary(): void {
    if (!summary.value) return
    const counters = { up: 0, down: 0, degraded: 0, pending: 0, maintenance: 0, unknown: 0, paused: 0 }
    for (const monitor of monitors.value) {
      if (!monitor.active) {
        counters.paused += 1
        continue
      }
      switch (monitor.status) {
        case 'up':
          counters.up += 1
          break
        case 'down':
          counters.down += 1
          break
        case 'degraded':
          counters.degraded += 1
          break
        case 'pending':
          counters.pending += 1
          break
        case 'maintenance':
          counters.maintenance += 1
          break
        default:
          counters.unknown += 1
      }
    }
    summary.value = { ...summary.value, ...counters, total: monitors.value.length }
  }

  return {
    monitors,
    summary,
    loading,
    state,
    reason,
    polling,
    connected,
    lastError,
    downCount,
    sorted,
    load,
    connect,
    disconnect,
    retryRealtime,
    upsert,
    remove,
    refreshSummary,
  }
})
