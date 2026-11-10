import type { RealtimeMessage } from '../types/protocol'

export type RealtimeTransportKind = 'webtransport' | 'websocket'

export interface RealtimeTransport {
  readonly kind: RealtimeTransportKind
  send(message: RealtimeMessage): void
  close(reason?: string): void
}

interface Callbacks {
  message: (message: RealtimeMessage) => void
  close: () => void
}

function isLoopback(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]'
}

function configuredURLs(name: 'VITE_REALTIME_URLS' | 'VITE_WEBTRANSPORT_URLS', legacy?: string): string[] {
  const value = (import.meta.env[name] || (legacy ? import.meta.env[legacy] : undefined)) as string | undefined
  return value?.split(',').map((item) => item.trim()).filter(Boolean) ?? []
}

function webTransportCertificateHashes(): WebTransportHash[] | undefined {
  const encoded = (import.meta.env.VITE_WEBTRANSPORT_CERT_SHA256 as string | undefined)?.trim()
  if (!encoded) return undefined
  try {
    const bytes = Uint8Array.from(atob(encoded), (character) => character.charCodeAt(0))
    if (bytes.length !== 32) throw new Error('invalid digest length')
    return [{ algorithm: 'sha-256', value: bytes }]
  } catch {
    throw new Error('VITE_WEBTRANSPORT_CERT_SHA256 must be a base64-encoded SHA-256 certificate digest.')
  }
}

function ownerURL(endpoint: string, path: string): URL {
  const url = new URL(endpoint, window.location.href)
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || url.username || url.password) throw new Error('The owner returned an invalid endpoint.')
  if (url.protocol === 'http:' && (window.location.protocol === 'https:' || !isLoopback(url.hostname))) throw new Error('The owner returned an insecure endpoint.')
  url.pathname = path
  url.search = ''
  url.hash = ''
  return url
}

export function websocketURL(endpoint?: string, seedIndex = 0): string {
  if (endpoint) {
    const url = ownerURL(endpoint, '/api/realtime')
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    return url.toString()
  }
  const configured = configuredURLs('VITE_REALTIME_URLS', 'VITE_REALTIME_URL')
  if (configured.length > 0) {
    const candidate = new URL(configured[seedIndex % configured.length])
    if ((candidate.protocol !== 'ws:' && candidate.protocol !== 'wss:') || candidate.username || candidate.password) throw new Error('The configured realtime endpoint is invalid.')
    if (candidate.protocol === 'ws:' && (window.location.protocol === 'https:' || !isLoopback(candidate.hostname))) throw new Error('The configured realtime endpoint is insecure.')
    return candidate.toString()
  }
  const url = new URL('/api/realtime', window.location.href)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}

export function webTransportURL(endpoint?: string, seedIndex = 0): string {
  if (endpoint) {
    const url = ownerURL(endpoint, '/api/webtransport')
    if (url.protocol !== 'https:') throw new Error('WebTransport requires an HTTPS owner endpoint.')
    return url.toString()
  }
  const configured = configuredURLs('VITE_WEBTRANSPORT_URLS')
  if (configured.length > 0) {
    const candidate = new URL(configured[seedIndex % configured.length])
    if (candidate.protocol !== 'https:' || candidate.username || candidate.password) throw new Error('The configured WebTransport endpoint must use HTTPS.')
    return candidate.toString()
  }
  const url = new URL('/api/webtransport', window.location.href)
  if (url.protocol !== 'https:') throw new Error('WebTransport requires an HTTPS page or an explicit HTTPS endpoint.')
  return url.toString()
}

class WebSocketTransport implements RealtimeTransport {
  readonly kind = 'websocket' as const
  private closed = false

  private constructor(private readonly socket: WebSocket) {}

  static connect(url: string, callbacks: Callbacks): Promise<WebSocketTransport> {
    return new Promise((resolve, reject) => {
      const socket = new WebSocket(url)
      let established = false
      let transport: WebSocketTransport
      socket.onopen = () => {
        established = true
        transport = new WebSocketTransport(socket)
        resolve(transport)
      }
      socket.onmessage = (event) => {
        try {
          callbacks.message(JSON.parse(String(event.data)) as RealtimeMessage)
        } catch {
          socket.close(1002, 'invalid server message')
        }
      }
      socket.onerror = () => {
        if (!established) reject(new Error('WebSocket connection failed.'))
        socket.close()
      }
      socket.onclose = () => {
        if (!established) reject(new Error('WebSocket connection closed during setup.'))
        else if (!transport.closed) callbacks.close()
      }
    })
  }

  send(message: RealtimeMessage): void {
    if (this.socket.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(message))
  }

  close(reason = 'session closed'): void {
    if (this.closed) return
    this.closed = true
    this.socket.close(1000, reason.slice(0, 123))
  }
}

class WebTransportStreamTransport implements RealtimeTransport {
  readonly kind = 'webtransport' as const
  private closed = false
  private writes: Promise<void> = Promise.resolve()

  private constructor(
    private readonly session: WebTransport,
    private readonly writer: WritableStreamDefaultWriter<Uint8Array>,
    private readonly reader: ReadableStreamDefaultReader<Uint8Array>,
    private readonly callbacks: Callbacks,
  ) {}

  static async connect(url: string, callbacks: Callbacks): Promise<WebTransportStreamTransport> {
    if (typeof WebTransport === 'undefined') throw new Error('WebTransport is not supported by this browser.')
    const serverCertificateHashes = webTransportCertificateHashes()
    const session = new WebTransport(url, serverCertificateHashes ? { serverCertificateHashes } : undefined)
    try {
      await session.ready
      const stream = await session.createBidirectionalStream()
      const transport = new WebTransportStreamTransport(session, stream.writable.getWriter(), stream.readable.getReader(), callbacks)
      void transport.readLoop()
      return transport
    } catch (error) {
      session.close({ closeCode: 0, reason: 'setup failed' })
      throw error
    }
  }

  send(message: RealtimeMessage): void {
    if (this.closed) return
    const payload = new TextEncoder().encode(JSON.stringify(message))
    const frame = new Uint8Array(payload.length + 4)
    new DataView(frame.buffer).setUint32(0, payload.length)
    frame.set(payload, 4)
    this.writes = this.writes.then(() => this.writer.write(frame)).catch(() => this.finish())
  }

  close(reason = 'session closed'): void {
    if (this.closed) return
    this.closed = true
    void this.reader.cancel(reason).catch(() => undefined)
    void this.writer.close().catch(() => undefined)
    this.closeSession(reason)
  }

  private async readLoop(): Promise<void> {
    let buffered = new Uint8Array(0)
    try {
      while (!this.closed) {
        const { value, done } = await this.reader.read()
        if (done) break
        const next = new Uint8Array(buffered.length + value.length)
        next.set(buffered)
        next.set(value, buffered.length)
        buffered = next
        while (buffered.length >= 4) {
          const size = new DataView(buffered.buffer, buffered.byteOffset, buffered.byteLength).getUint32(0)
          if (size === 0 || size > 1 << 20) throw new Error('Invalid WebTransport message size.')
          if (buffered.length < size + 4) break
          const payload = buffered.slice(4, size + 4)
          this.callbacks.message(JSON.parse(new TextDecoder().decode(payload)) as RealtimeMessage)
          buffered = buffered.slice(size + 4)
        }
      }
    } catch {
      // The shared session state machine handles reconnect and recovery.
    }
    this.finish()
  }

  private finish(): void {
    if (this.closed) return
    this.closed = true
    this.closeSession('stream closed')
    this.callbacks.close()
  }

  private closeSession(reason: string): void {
    try {
      this.session.close({ closeCode: 0, reason: reason.slice(0, 1024) })
    } catch {
      // Closing an already-closed browser session is an expected race between
      // the stream read loop, offline handling, and an explicit editor close.
    }
  }
}

export async function connectRealtimeTransport(endpoint: string | undefined, seedIndex: number, callbacks: Callbacks): Promise<RealtimeTransport> {
  const preference = ((import.meta.env.VITE_REALTIME_TRANSPORT as string | undefined) ?? 'auto').toLowerCase()
  if (preference !== 'auto' && preference !== 'webtransport' && preference !== 'websocket') throw new Error('VITE_REALTIME_TRANSPORT must be auto, webtransport, or websocket.')
  if (preference !== 'websocket') {
	const configured = configuredURLs('VITE_WEBTRANSPORT_URLS').length > 0
	const secureEndpoint = endpoint ? new URL(endpoint, window.location.href).protocol === 'https:' : window.location.protocol === 'https:'
	const unavailable = typeof WebTransport === 'undefined' || (preference === 'auto' && !configured) || (preference === 'webtransport' && !configured && !secureEndpoint)
	if (unavailable) {
	  if (preference === 'webtransport') throw new Error('WebTransport is unavailable or has no HTTPS endpoint configured.')
	} else {
	  // Once the API is available, setup failures may represent TLS, Origin, or
	  // network policy failures. Never hide those security failures by silently
	  // downgrading to WebSocket.
	  return WebTransportStreamTransport.connect(webTransportURL(endpoint, seedIndex), callbacks)
	}
  }
  return WebSocketTransport.connect(websocketURL(endpoint, seedIndex), callbacks)
}

export function realtimeSeedCount(): number {
  return Math.max(configuredURLs('VITE_WEBTRANSPORT_URLS').length, configuredURLs('VITE_REALTIME_URLS', 'VITE_REALTIME_URL').length, 1)
}
