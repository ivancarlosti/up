import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { systemAlerting, systemOverThreshold } from '@/lib/system-sort'
import { useAppStore } from '@/stores/app'
import type {
  SmartReport,
  System,
  SystemAlerts,
  SystemAlertsPayload,
  SystemContainer,
  SystemHistory,
  SystemPayload,
  SystemPointOverview,
} from '@/lib/types-systems'

/**
 * useSystemStore keeps the hardware monitored machines, their containers and their
 * disks.
 *
 * It is deliberately a plain pull store (unlike the monitors store, which applies a
 * stream of heartbeats): a machine reports once a minute, so a refresh on mount and
 * after a mutation is enough, and the live events only bump a counter that tells
 * the page it should reload.
 */
/**
 * containerRunning reports whether the status the agent reported means the
 * container is running.
 *
 * Docker reports a human string ("Up 2 days", "Exited (0) 3 hours ago"), not the
 * state field, so a comparison against "running" would mark every container as
 * stopped.
 */
export function containerRunning(status?: string): boolean {
  const value = (status ?? '').trim().toLowerCase()
  return value === 'running' || value.startsWith('up')
}

export const useSystemStore = defineStore('systems', () => {
  const systems = ref<System[]>([])
  const containers = ref<SystemContainer[]>([])
  const smart = ref<SmartReport[]>([])
  /**
   * overview is the LAST SAMPLE of every machine, keyed by system id.
   *
   * It is what the table shows under each name, and what the "High CPU/memory/
   * disk" boxes count: a machine row carries no counters of its own, and asking
   * every row for them would be one request per machine.
   */
  const overview = ref<Map<number, SystemPointOverview>>(new Map())
  const loading = ref(false)
  const saving = ref(false)
  const lastError = ref<string | null>(null)

  const upCount = computed(() => systems.value.filter((system) => system.status === 'up').length)
  const pendingCount = computed(() => systems.value.filter((system) => system.status === 'pending').length)
  const downCount = computed(() => systems.value.filter((system) => system.status === 'down').length)
  const runningContainers = computed(() => containers.value.filter((container) => containerRunning(container.status)).length)
  /**
   * The four alert counters of the summary row.
   *
   * They are deliberately independent of each other: a machine can be down AND
   * over its disk threshold, and the boxes are filters, not a breakdown. The
   * numbers are computed from the same helpers the table uses, so a badge and a
   * box never disagree.
   */
  const alertingCount = computed(() => systems.value.filter(systemAlerting).length)
  const highCpuCount = computed(
    () => systems.value.filter((system) => systemOverThreshold(system, 'cpu', overview.value)).length,
  )
  const highMemoryCount = computed(
    () => systems.value.filter((system) => systemOverThreshold(system, 'memory', overview.value)).length,
  )
  const highDiskCount = computed(
    () => systems.value.filter((system) => systemOverThreshold(system, 'disk', overview.value)).length,
  )

  /** load fetches the machine list; silent skips the spinner of a refresh. */
  async function load(options: { silent?: boolean } = {}): Promise<void> {
    if (!options.silent) loading.value = true
    lastError.value = null
    try {
      systems.value = await api.systems()
    } catch (error) {
      lastError.value = translateError(error)
    } finally {
      loading.value = false
    }
  }

  /** loadContainers fetches every container of every machine. */
  async function loadContainers(): Promise<void> {
    loading.value = true
    lastError.value = null
    try {
      containers.value = await api.allContainers()
    } catch (error) {
      lastError.value = translateError(error)
    } finally {
      loading.value = false
    }
  }

  /** loadSmart fetches the stored SMART reports. */
  async function loadSmart(): Promise<void> {
    loading.value = true
    lastError.value = null
    try {
      smart.value = await api.smart()
    } catch (error) {
      lastError.value = translateError(error)
    } finally {
      loading.value = false
    }
  }

  /** history loads one machine with its charts, containers and disks. */
  async function history(id: number, hours: number): Promise<SystemHistory> {
    return api.systemHistory(id, hours)
  }

  /**
   * loadCounters fills the two badges the page shows next to its tabs.
   *
   * The bodies of "All containers" and "S.M.A.R.T." are only fetched when their
   * tab is opened, so without this the labels read 0 on the first visit - which
   * looks exactly like an instance where nothing runs. It stays silent (no
   * spinner, no error toast) because the tab reports both when it does load.
   */
  async function loadCounters(): Promise<void> {
    try {
      const [rows, reports] = await Promise.all([api.allContainers(), api.smart()])
      containers.value = rows
      smart.value = reports
    } catch {
      // The badges are decoration: the tab body owns the message when it fails.
    }
  }

  /** loadOverview fetches the last sample of every machine. */
  async function loadOverview(): Promise<void> {
    try {
      const rows = await api.systemsOverview()
      overview.value = new Map(rows.map((row) => [row.system_id, row]))
    } catch {
      // The counters are decoration: the table reports its own failure, and an
      // empty map simply renders a dash instead of a number.
      overview.value = new Map()
    }
  }

  /** overviewOf returns the last sample of a machine (undefined while none exists). */
  function overviewOf(id: number): SystemPointOverview | undefined {
    return overview.value.get(id)
  }

  /** alerts loads the alert configuration of one machine (the dialog payload). */
  async function alerts(id: number): Promise<SystemAlerts> {
    return api.systemAlerts(id)
  }

  /** saveAlerts stores the rules and the channel links of one machine. */
  async function saveAlerts(id: number, payload: SystemAlertsPayload): Promise<SystemAlerts> {
    saving.value = true
    try {
      const saved = await api.saveSystemAlerts(id, payload)
      await load({ silent: true })
      return saved
    } finally {
      saving.value = false
    }
  }

  /** resetAlerts restores the global default rules on one machine. */
  async function resetAlerts(id: number): Promise<SystemAlerts> {
    saving.value = true
    try {
      const view = await api.resetSystemAlerts(id)
      await load({ silent: true })
      return view
    } finally {
      saving.value = false
    }
  }

  /** copyAlerts copies the rules of another machine onto one machine. */
  async function copyAlerts(id: number, sourceId: number): Promise<SystemAlerts> {
    saving.value = true
    try {
      const view = await api.copySystemAlerts(id, sourceId)
      await load({ silent: true })
      return view
    } finally {
      saving.value = false
    }
  }

  /** create stores a machine and answers with its one time token. */
  async function create(payload: SystemPayload): Promise<System> {
    saving.value = true
    try {
      const created = await api.createSystem(payload)
      await load({ silent: true })
      return created
    } finally {
      saving.value = false
    }
  }

  async function update(id: number, payload: SystemPayload): Promise<System> {
    saving.value = true
    try {
      const updated = await api.updateSystem(id, payload)
      await load({ silent: true })
      return updated
    } finally {
      saving.value = false
    }
  }

  async function remove(id: number): Promise<void> {
    saving.value = true
    try {
      await api.deleteSystem(id)
      systems.value = systems.value.filter((system) => system.id !== id)
    } finally {
      saving.value = false
    }
  }

  /** rotateToken replaces the agent token (the agent must be restarted after it). */
  async function rotateToken(id: number): Promise<System> {
    saving.value = true
    try {
      const rotated = await api.rotateSystemToken(id)
      await load({ silent: true })
      return rotated
    } finally {
      saving.value = false
    }
  }

  /**
   * startPolling refreshes the visible list on a timer.
   *
   * A machine reports once a minute, so a socket per page would add a second
   * channel for a payload that is already derived: polling the list while the page
   * is open is enough, and it stops the moment the page is left.
   */
  let timer: ReturnType<typeof setInterval> | undefined
  let currentTab: 'systems' | 'containers' | 'smart' = 'systems'
  function startPolling(tab: 'systems' | 'containers' | 'smart', intervalMs = 30000): void {
    currentTab = tab
    stopPolling()
    timer = setInterval(() => {
      void refresh(currentTab)
    }, intervalMs)
  }
  function stopPolling(): void {
    if (timer) clearInterval(timer)
    timer = undefined
  }

  /** refresh reloads whatever the caller is showing. */
  async function refresh(tab: 'systems' | 'containers' | 'smart'): Promise<void> {
    if (tab === 'containers') return loadContainers()
    if (tab === 'smart') return loadSmart()
    await Promise.all([load({ silent: true }), loadOverview()])
  }

  const app = useAppStore()
  const clusterEnabled = computed(() => app.settings?.cluster_enabled ?? false)

  return {
    systems,
    containers,
    smart,
    overview,
    loading,
    saving,
    lastError,
    upCount,
    downCount,
    pendingCount,
    alertingCount,
    highCpuCount,
    highMemoryCount,
    highDiskCount,
    runningContainers,
    clusterEnabled,
    load,
    loadOverview,
    overviewOf,
    loadContainers,
    loadSmart,
    loadCounters,
    history,
    alerts,
    saveAlerts,
    resetAlerts,
    copyAlerts,
    create,
    update,
    remove,
    rotateToken,
    startPolling,
    stopPolling,
    refresh,
  }
})
