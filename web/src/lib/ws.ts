/**
 * ws.ts implements the dashboard real time channel.
 *
 * The socket reconnects with a jittered backoff, sends a ping every 25s (the hub
 * answers with a pong and drops silent clients) and fans out the events to the
 * subscribers. It also exposes an honest connection state: "reconnecting" only
 * means a retry is actually pending, and after too many failures the channel
 * gives up and reports why (session expired, hub disabled, proxy that does not
 * forward the upgrade, ...), which is what the header badge shows.
 */

export interface RealtimeEvent {
  type: string
  payload?: unknown
  at: string
}

/** Connection state of the real time channel. */
export type RealtimeState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'unavailable'

/** Why the channel is unavailable (translated by the UI). */
export type RealtimeReason = '' | 'auth' | 'disabled' | 'proxy' | 'route' | 'network'

type Handler = (event: RealtimeEvent) => void

type StateHandler = (state: RealtimeState, reason: RealtimeReason) => void

/** Failures tolerated before the channel gives up and asks for polling. */
const MAX_ATTEMPTS = 5

const PING_INTERVAL = 25000

export class RealtimeClient {
  private socket: WebSocket | null = null
  private handlers = new Map<string, Set<Handler>>()
  private stateHandlers = new Set<StateHandler>()
  private reconnectDelay = 1000
  private attempts = 0
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined
  private pingTimer: ReturnType<typeof setInterval> | undefined
  private closedByUser = false
  private state: RealtimeState = 'idle'
  private reason: RealtimeReason = ''

  /** topics defaults to everything the server publishes. */
  constructor(private readonly topics: string[] = ['*']) {
    if (typeof window !== 'undefined') {
      // Returning to the tab or regaining the network is a good moment to retry
      // immediately instead of waiting for the next backoff step.
      window.addEventListener('online', this.wake)
      document.addEventListener('visibilitychange', this.wake)
    }
  }

  getState(): RealtimeState {
    return this.state
  }

  getReason(): RealtimeReason {
    return this.reason
  }

  connect(): void {
    if (typeof WebSocket === 'undefined') return
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return
    }
    this.closedByUser = false
    this.setState(this.attempts === 0 ? 'connecting' : 'reconnecting')

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/api/ws`
    this.socket = new WebSocket(url)

    this.socket.onopen = () => {
      this.reconnectDelay = 1000
      this.attempts = 0
      this.setState('open')
      this.send({ action: 'subscribe', topics: this.topics })
      this.pingTimer = setInterval(() => this.send({ action: 'ping' }), PING_INTERVAL)
    }

    this.socket.onmessage = (message) => {
      try {
        const event = JSON.parse(message.data as string) as RealtimeEvent
        if (event.type === 'pong') return
        this.emit(event)
      } catch {
        /* ignore malformed frames */
      }
    }

    this.socket.onclose = () => {
      if (this.pingTimer) clearInterval(this.pingTimer)
      if (this.closedByUser) {
        this.setState('idle')
        return
      }
      this.scheduleReconnect()
    }

    this.socket.onerror = () => {
      this.socket?.close()
    }
  }

  /** on subscribes to an event type ("*" receives everything). */
  on(type: string, handler: Handler): () => void {
    if (!this.handlers.has(type)) this.handlers.set(type, new Set())
    this.handlers.get(type)?.add(handler)
    return () => this.handlers.get(type)?.delete(handler)
  }

  /** onState observes the connection state (used for the header badge). */
  onState(handler: StateHandler): () => void {
    this.stateHandlers.add(handler)
    handler(this.state, this.reason)
    return () => this.stateHandlers.delete(handler)
  }

  /**
   * retry restarts the channel after it gave up (the store calls it from the
   * polling fallback, which is also a way back to real time updates).
   */
  retry(): void {
    if (this.state !== 'unavailable') return
    this.attempts = 0
    this.reconnectDelay = 1000
    this.connect()
  }

  /** close stops the client and cancels the reconnection. */
  close(): void {
    this.closedByUser = true
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    if (this.pingTimer) clearInterval(this.pingTimer)
    if (typeof window !== 'undefined') {
      window.removeEventListener('online', this.wake)
      document.removeEventListener('visibilitychange', this.wake)
    }
    this.socket?.close()
    this.socket = null
    this.attempts = 0
    this.setState('idle')
  }

  private emit(event: RealtimeEvent): void {
    this.handlers.get(event.type)?.forEach((handler) => handler(event))
    this.handlers.get('*')?.forEach((handler) => handler(event))
  }

  private setState(state: RealtimeState, reason: RealtimeReason = ''): void {
    this.state = state
    this.reason = reason
    this.stateHandlers.forEach((handler) => handler(state, reason))
  }

  private send(payload: unknown): void {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(payload))
    }
  }

  /** wake retries as soon as the tab or the network comes back. */
  private wake = (): void => {
    if (this.closedByUser) return
    if (this.state === 'unavailable' || this.state === 'reconnecting') {
      if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
      this.attempts = 0
      this.reconnectDelay = 1000
      this.connect()
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    this.attempts += 1

    // The first failure is a good moment to ask the endpoint why the upgrade
    // fails: an expired session, a disabled hub or a proxy that drops the
    // Upgrade header are not going to fix themselves, so the channel reports
    // itself unavailable immediately and the store starts polling instead of
    // pretending a reconnection is on its way.
    if (this.attempts === 1) {
      void this.explain()
      return
    }
    if (this.attempts > MAX_ATTEMPTS) {
      this.setState('unavailable', this.reason || 'network')
      return
    }
    this.setState('reconnecting')

    // Jitter avoids every tab of every admin retrying in lockstep after a
    // restart of the backend.
    const jitter = 1 + (Math.random() * 0.4 - 0.2)
    const delay = Math.min(this.reconnectDelay * jitter, 30000)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, 30000)
      this.connect()
    }, delay)
  }

  /** explain probes the endpoint and reacts to fatally broken setups. */
  private async explain(): Promise<void> {
    const { reason, fatal } = await this.probe()
    if (this.closedByUser) return
    this.reason = reason
    if (fatal) {
      this.setState('unavailable', reason)
      return
    }
    this.setState('reconnecting')
    const jitter = 1 + (Math.random() * 0.4 - 0.2)
    this.reconnectTimer = setTimeout(() => this.connect(), this.reconnectDelay * jitter)
  }

  private async probe(): Promise<{ reason: RealtimeReason; fatal: boolean }> {
    try {
      const response = await fetch('/api/ws', { credentials: 'include', headers: { Accept: 'application/json' } })
      if (response.status === 401 || response.status === 403) return { reason: 'auth', fatal: true }
      // 400/426 come from a request that reached the server without the upgrade
      // (nginx without `map $http_upgrade $connection_upgrade`).
      if (response.status === 400 || response.status === 426) return { reason: 'proxy', fatal: true }
      if (response.status === 404) return { reason: 'route', fatal: true }
      if (response.status === 503) {
        const body = await response.text().catch(() => '')
        if (/disabled/i.test(body)) return { reason: 'disabled', fatal: true }
      }
      // Anything else (5xx while the backend restarts, a network error) is
      // transient: keep retrying with the backoff.
      return { reason: 'network', fatal: false }
    } catch {
      return { reason: 'network', fatal: false }
    }
  }
}
