import { afterEach, describe, expect, it, vi } from 'vitest'
import { connectRealtimeTransport, webTransportURL, websocketURL } from './realtimeTransport'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('realtime transport selection', () => {
  it('builds transport-specific owner URLs without trusting credentials', () => {
    expect(websocketURL('https://owner.example.com:8080')).toBe('wss://owner.example.com:8080/api/realtime')
    expect(webTransportURL('https://owner.example.com:8080')).toBe('https://owner.example.com:8080/api/webtransport')
    expect(() => websocketURL('https://user:password@owner.example.com')).toThrow(/invalid endpoint/)
    expect(() => webTransportURL('http://owner.example.com')).toThrow(/insecure endpoint/)
  })

  it('falls back to WebSocket when WebTransport is unsupported', async () => {
    class FakeWebSocket {
      static readonly OPEN = 1
      readonly readyState = FakeWebSocket.OPEN
      onopen: (() => void) | null = null
      onmessage: ((event: { data: string }) => void) | null = null
      onerror: (() => void) | null = null
      onclose: (() => void) | null = null

      constructor(readonly url: string) {
        queueMicrotask(() => this.onopen?.())
      }

      send(): void {}
      close(): void {}
    }

    vi.stubEnv('VITE_REALTIME_TRANSPORT', 'auto')
    vi.stubGlobal('WebTransport', undefined)
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const transport = await connectRealtimeTransport(undefined, 0, { message: () => undefined, close: () => undefined })
    expect(transport.kind).toBe('websocket')
    transport.close()
  })

  it('uses WebSocket when auto mode has no explicit WebTransport endpoint', async () => {
	class FakeWebSocket {
	  static readonly OPEN = 1
	  readonly readyState = 1
	  onopen: (() => void) | null = null
	  onmessage = null
	  onerror = null
	  onclose = null
	  constructor() { queueMicrotask(() => this.onopen?.()) }
	  send(): void {}
	  close(): void {}
	}
	vi.stubEnv('VITE_REALTIME_TRANSPORT', 'auto')
	vi.stubGlobal('WebTransport', class {})
	vi.stubGlobal('WebSocket', FakeWebSocket)
	const transport = await connectRealtimeTransport(undefined, 0, { message: () => undefined, close: () => undefined })
	expect(transport.kind).toBe('websocket')
	transport.close()
  })

  it('does not silently downgrade when WebTransport is required', async () => {
    vi.stubEnv('VITE_REALTIME_TRANSPORT', 'webtransport')
	vi.stubEnv('VITE_WEBTRANSPORT_URLS', 'https://realtime.example.com/api/webtransport')
    vi.stubGlobal('WebTransport', undefined)
    await expect(connectRealtimeTransport(undefined, 0, { message: () => undefined, close: () => undefined })).rejects.toThrow(/unavailable/)
  })

  it('does not downgrade TLS or policy failures when WebTransport is available', async () => {
	class FailingWebTransport {
	  readonly ready = Promise.reject(new Error('TLS validation failed'))
	  close(): void {}
	}
	const websocketConstructor = vi.fn()
	vi.stubEnv('VITE_REALTIME_TRANSPORT', 'auto')
	vi.stubEnv('VITE_WEBTRANSPORT_URLS', 'https://realtime.example.com/api/webtransport')
	vi.stubGlobal('WebTransport', FailingWebTransport)
	vi.stubGlobal('WebSocket', websocketConstructor)
	await expect(connectRealtimeTransport(undefined, 0, { message: () => undefined, close: () => undefined })).rejects.toThrow(/TLS validation failed/)
	expect(websocketConstructor).not.toHaveBeenCalled()
  })

  it('passes a configured certificate digest to WebTransport', async () => {
    let receivedOptions: WebTransportOptions | undefined
    class FakeWebTransport {
      readonly ready = Promise.resolve()
      constructor(_url: string, options?: WebTransportOptions) { receivedOptions = options }
      createBidirectionalStream(): Promise<WebTransportBidirectionalStream> {
        return Promise.resolve({
          readable: new ReadableStream({ start: (controller) => controller.close() }),
          writable: new WritableStream(),
        })
      }
      close(): void {}
    }
    const digest = Uint8Array.from({ length: 32 }, (_, index) => index)
    vi.stubEnv('VITE_REALTIME_TRANSPORT', 'webtransport')
    vi.stubEnv('VITE_WEBTRANSPORT_URLS', 'https://realtime.example.com/api/webtransport')
    vi.stubEnv('VITE_WEBTRANSPORT_CERT_SHA256', btoa(String.fromCharCode(...digest)))
    vi.stubGlobal('WebTransport', FakeWebTransport)

    const transport = await connectRealtimeTransport(undefined, 0, { message: () => undefined, close: () => undefined })
    expect(receivedOptions?.serverCertificateHashes).toHaveLength(1)
    expect(receivedOptions?.serverCertificateHashes?.[0].algorithm).toBe('sha-256')
    const receivedDigest = receivedOptions?.serverCertificateHashes?.[0].value
    expect(receivedDigest).toBeInstanceOf(Uint8Array)
    expect(Array.from(receivedDigest as Uint8Array)).toEqual(Array.from(digest))
    transport.close()
  })

  it('rejects a malformed certificate digest', async () => {
    vi.stubEnv('VITE_REALTIME_TRANSPORT', 'webtransport')
    vi.stubEnv('VITE_WEBTRANSPORT_URLS', 'https://realtime.example.com/api/webtransport')
    vi.stubEnv('VITE_WEBTRANSPORT_CERT_SHA256', 'not-a-sha256-digest')
    vi.stubGlobal('WebTransport', class {})
    await expect(connectRealtimeTransport(undefined, 0, { message: () => undefined, close: () => undefined })).rejects.toThrow(/base64-encoded SHA-256/)
  })
})
