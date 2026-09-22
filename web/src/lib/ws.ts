/**
 * ws.ts implements the dashboard real time channel.
 *
 * The socket reconnects automatically with a backoff, sends a ping every 25s
 * (the hub answers with a pong and drops silent clients) and fans out the events
 * to the subscribers. Components use the reactive state in the Pinia store
 * instead of talking to this class directly.
 */

export interface RealtimeEvent {
  type: string
  payload?: unknown
  at: string
}

type Handler = (event: RealtimeEvent) => void

export class RealtimeClient {
  private socket: WebSocket | null = null
  private handlers = new Map<string, Set<Handler>>()
  private reconnectDelay = 1000
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined
  private pingTimer: ReturnType<typeof setInterval> | undefined
  private closedByUser = false
  private statusHandlers = new Set<(connected: boolean) => void>()

  /** topics defaults to everything the server publishes. */
  constructor(private readonly topics: string[] = ['*']) {}

  connect(): void {
    if (typeof WebSocket === 'undefined') return
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return
    }
    this.closedByUser = false

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/api/ws`
    this.socket = new WebSocket(url)

    this.socket.onopen = () => {
      this.reconnectDelay = 1000
      this.notifyStatus(true)
      this.send({ action: 'subscribe', topics: this.topics })
      this.pingTimer = setInterval(() => this.send({ action: 'ping' }), 25000)
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
      this.notifyStatus(false)
      if (this.pingTimer) clearInterval(this.pingTimer)
      if (!this.closedByUser) this.scheduleReconnect()
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

  /** onStatus observes the connection state (used for the "live" indicator). */
  onStatus(handler: (connected: boolean) => void): () => void {
    this.statusHandlers.add(handler)
    return () => this.statusHandlers.delete(handler)
  }

  /** close stops the client and cancels the reconnection. */
  close(): void {
    this.closedByUser = true
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    if (this.pingTimer) clearInterval(this.pingTimer)
    this.socket?.close()
    this.socket = null
  }

  private emit(event: RealtimeEvent): void {
    this.handlers.get(event.type)?.forEach((handler) => handler(event))
    this.handlers.get('*')?.forEach((handler) => handler(event))
  }

  private notifyStatus(connected: boolean): void {
    this.statusHandlers.forEach((handler) => handler(connected))
  }

  private send(payload: unknown): void {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(payload))
    }
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, 30000)
      this.connect()
    }, this.reconnectDelay)
  }
}
