import { useAuthStore } from '../stores/authStore'
import { getAccessToken } from '../api/client'
import { useCollaborationStore } from '../stores/collaborationStore'
import type { CursorState, RealtimeMessage } from '../types/protocol'
import { createRequestId } from '../utils/requestId'
import { applyOperation, diffOperations, transformOperation } from './operations'
import { commitRecoveryState, loadPending, loadRecoveryState, savePending, type PendingEdit } from './pendingEdits'

const maxRedirects = 5
const cursorThrottleMs = 50
const cursorHeartbeatMs = 10_000

export interface RealtimeSessionObserver {
  cursorChanged?: (cursor: CursorState) => void
  cursorRemoved?: (userID: string) => void
}

function isLoopback(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]'
}

function configuredGatewayURLs(): string[] {
  const configured = (import.meta.env.VITE_REALTIME_URLS || import.meta.env.VITE_REALTIME_URL) as string | undefined
  return configured?.split(',').map((value) => value.trim()).filter(Boolean) ?? []
}

function gatewayURL(endpoint?: string, seedIndex = 0): string {
  if (endpoint) {
    const url = new URL(endpoint, window.location.href)
    if ((url.protocol !== 'http:' && url.protocol !== 'https:') || url.username || url.password) throw new Error('The owner returned an invalid endpoint.')
    if (url.protocol === 'http:' && (window.location.protocol === 'https:' || !isLoopback(url.hostname))) throw new Error('The owner returned an insecure endpoint.')
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    url.pathname = '/api/realtime'
    url.search = ''
    return url.toString()
  }
  const configured = configuredGatewayURLs()
  if (configured.length > 0) {
    const candidate = new URL(configured[seedIndex % configured.length])
    if ((candidate.protocol !== 'ws:' && candidate.protocol !== 'wss:') || candidate.username || candidate.password) {
      throw new Error('The configured realtime endpoint is invalid.')
    }
    if (candidate.protocol === 'ws:' && (window.location.protocol === 'https:' || !isLoopback(candidate.hostname))) {
      throw new Error('The configured realtime endpoint is insecure.')
    }
    return candidate.toString()
  }
  const url = new URL('/api/realtime', window.location.href)
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}

export class RealtimeSession {
  private socket: WebSocket | null = null
  private stopped = false
  private reconnectAttempt = 0
  private redirectCount = 0
  private endpoint = ''
  private seedIndex = 0
  private readonly visitedEndpoints = new Set<string>()
  private pending: PendingEdit[] = []
  private inFlight = ''
  private confirmedContent = ''
  private confirmedVersion = 0
  private hasJoined = false
  private authRefreshAttempted = false
  private followingRedirect = false
  private tasks: Promise<void> = Promise.resolve()
  private reconnectTimer: number | null = null
  private cursorTimer: number | null = null
  private cursorHeartbeat: number | null = null
  private cursorSequence = 0
  private lastCursorSentAt = 0
  private currentCursor: Pick<CursorState, 'anchor' | 'head'> | null = null
  private localChangesQueued = 0

  constructor(
    private readonly documentId: string,
    private readonly userId: string,
    private readonly observer: RealtimeSessionObserver = {},
  ) {}

  async start(): Promise<void> {
    this.stopped = false
    window.addEventListener('offline', this.handleOffline)
    window.addEventListener('online', this.handleOnline)
    const [pending, recoveryState] = await Promise.all([
      loadPending(this.userId, this.documentId),
      loadRecoveryState(this.userId, this.documentId),
    ])
    this.pending = pending
    const state = useCollaborationStore.getState()
    this.confirmedContent = recoveryState?.content ?? state.content
    this.confirmedVersion = recoveryState?.version ?? state.version
    this.hasJoined = recoveryState !== null
    state.setDocument(this.withPending(this.confirmedContent), this.confirmedVersion)
    this.updatePendingCount()
    this.cursorHeartbeat = window.setInterval(() => this.sendCurrentCursor(true), cursorHeartbeatMs)
    this.connect(false)
  }

  stop(): void {
    this.stopped = true
    window.removeEventListener('offline', this.handleOffline)
    window.removeEventListener('online', this.handleOnline)
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    if (this.cursorTimer !== null) window.clearTimeout(this.cursorTimer)
    if (this.cursorHeartbeat !== null) window.clearInterval(this.cursorHeartbeat)
    this.cursorTimer = null
    this.cursorHeartbeat = null
    this.socket?.close(1000, 'editor closed')
    this.socket = null
  }

  private readonly handleOffline = (): void => {
    if (this.stopped) return
    useCollaborationStore.getState().setConnection('RECONNECTING')
    const socket = this.socket
    this.socket = null
    this.inFlight = ''
    socket?.close()
  }

  private readonly handleOnline = (): void => {
    if (this.stopped || this.socket !== null || this.reconnectTimer !== null) return
    this.connect(true)
  }

  async updateContent(next: string): Promise<void> {
    this.localChangesQueued += 1
    const update = this.tasks
      .catch(() => undefined)
      .then(() => this.persistLocalChange(next))
      .finally(() => {
        this.localChangesQueued -= 1
        if (this.localChangesQueued === 0) this.scheduleCursor()
      })
    this.tasks = update.catch((error: unknown) => {
      useCollaborationStore.getState().setError(error instanceof Error ? error.message : 'Unable to save the local edit.')
    })
    return update
  }

  updateCursor(anchor: number, head: number): void {
    this.currentCursor = { anchor, head }
    this.scheduleCursor()
  }

  private async persistLocalChange(next: string): Promise<void> {
    const state = useCollaborationStore.getState()
    if (state.role === 'read' || state.role === '') return
    const operations = diffOperations(state.content, next)
    if (operations.length === 0) return
    let optimistic = state.content
    for (const operation of operations) {
      const pending: PendingEdit = {
        requestId: createRequestId('edit'),
        userId: this.userId,
        documentId: this.documentId,
        operation,
        createdAt: Date.now() + this.pending.length,
      }
      await savePending(pending)
      this.pending.push(pending)
      optimistic = applyOperation(optimistic, operation)
    }
    state.setDocument(optimistic, state.version)
    this.updatePendingCount()
    this.sendNext()
  }

  private connect(reconnecting: boolean): void {
    if (this.stopped) return
    useCollaborationStore.getState().setConnection(reconnecting ? 'RECONNECTING' : 'CONNECTING')
    let target: string
    try {
      target = gatewayURL(this.endpoint || undefined, this.seedIndex)
    } catch (error) {
      useCollaborationStore.getState().setError(error instanceof Error ? error.message : 'The owner returned an invalid endpoint.')
      this.stop()
      return
    }
    const socket = new WebSocket(target)
    this.socket = socket
    socket.onopen = () => {
      this.reconnectAttempt = 0
      useCollaborationStore.getState().setConnection('AUTHENTICATING')
      socket.send(JSON.stringify({ type: 'auth', token: getAccessToken() }))
    }
    socket.onmessage = (event) => {
      let message: RealtimeMessage
      try {
        message = JSON.parse(String(event.data)) as RealtimeMessage
      } catch {
        useCollaborationStore.getState().setError('The realtime server returned an invalid message.')
        socket.close(1002, 'invalid server message')
        return
      }
      this.tasks = this.tasks
        .catch(() => undefined)
        .then(() => this.receive(message))
        .catch((error: unknown) => {
          useCollaborationStore.getState().setError(error instanceof Error ? error.message : 'Realtime recovery failed.')
          socket.close(1011, 'recovery failed')
        })
    }
    socket.onerror = () => socket.close()
    socket.onclose = () => {
      if (this.socket !== socket) return
      this.socket = null
      this.inFlight = ''
      if (this.followingRedirect) {
        this.followingRedirect = false
      } else if (this.endpoint !== '') {
        // Re-enter through the configured seed after an owner connection fails
        // so the cluster can resolve a replacement owner.
        this.endpoint = ''
        this.redirectCount = 0
        this.visitedEndpoints.clear()
      } else {
        this.advanceSeed()
      }
      if (!this.stopped) this.scheduleReconnect()
    }
  }

  private async receive(message: RealtimeMessage): Promise<void> {
    const state = useCollaborationStore.getState()
    switch (message.type) {
      case 'auth_ok':
        this.authRefreshAttempted = false
        state.setConnection('SYNCING')
        this.send({ type: this.hasJoined ? 'resume' : 'join', doc_id: this.documentId, last_version: this.confirmedVersion })
        break
      case 'joined':
        this.confirmedContent = message.content ?? ''
        this.confirmedVersion = message.version ?? 0
        this.hasJoined = true
        await this.commitLocalState()
        state.initialize(this.documentId, this.withPending(this.confirmedContent), this.confirmedVersion)
        if (message.role) state.setRole(message.role)
        this.resetRedirectChain()
        state.setConnection('CONNECTED')
        this.sendCurrentCursor(true)
        this.sendNext()
        break
      case 'sync':
        await this.applyVersioned(message.operations ?? [], message.to_version ?? state.version)
        if (message.role) state.setRole(message.role)
        this.resetRedirectChain()
        state.setConnection('CONNECTED')
        this.sendCurrentCursor(true)
        this.sendNext()
        break
      case 'resync_required': {
        this.confirmedContent = message.snapshot?.content ?? ''
        this.confirmedVersion = message.snapshot?.version ?? 0
        await this.applyVersioned(message.operations ?? [], message.current_version ?? this.confirmedVersion)
        if (message.role) state.setRole(message.role)
        this.resetRedirectChain()
        state.setConnection('CONNECTED')
        this.sendCurrentCursor(true)
        this.sendNext()
        break
      }
      case 'edit_applied':
        await this.applyEdit(message)
        break
      case 'permission_changed':
        if (message.role) state.setRole(message.role)
        break
      case 'presence_changed':
        state.setParticipants(message.user_ids ?? [])
        break
      case 'cursor_changed':
        if (message.cursor?.user_id && message.cursor.user_id !== this.userId) this.observer.cursorChanged?.(message.cursor)
        break
      case 'cursor_removed':
        if (message.cursor?.user_id) this.observer.cursorRemoved?.(message.cursor.user_id)
        break
      case 'owner_redirect':
        if (!message.endpoint || this.redirectCount >= maxRedirects) {
          state.setError('Unable to locate the document owner.')
          this.stop()
          return
        }
        try {
          const redirected = gatewayURL(message.endpoint)
          if (this.visitedEndpoints.has(redirected)) {
            state.setError('An owner redirect loop was detected.')
            this.stop()
            return
          }
          this.visitedEndpoints.add(redirected)
        } catch (error) {
          state.setError(error instanceof Error ? error.message : 'The owner returned an invalid endpoint.')
          this.stop()
          return
        }
        this.redirectCount += 1
        this.endpoint = message.endpoint
        this.followingRedirect = true
        this.socket?.close(1000, 'following owner redirect')
        break
      case 'error':
        if (message.code === 'RESYNC_REQUIRED') { this.send({ type: 'resume', doc_id: this.documentId, last_version: 0 }); return }
        if (message.code === 'PERMISSION_DENIED' && !this.authRefreshAttempted) {
          this.authRefreshAttempted = true
          void useAuthStore.getState().refresh()
            .then(() => this.socket?.close(1000, 'token refreshed'))
            .catch(() => {
              state.setError('Your session is no longer active.')
              this.stop()
            })
          return
        }
        state.setError(message.message ?? 'Realtime collaboration failed.')
        if (message.code === 'PERMISSION_DENIED') this.stop()
        break
    }
  }

  private async applyVersioned(operations: NonNullable<RealtimeMessage['operations']>, version: number): Promise<void> {
    const state = useCollaborationStore.getState()
    const acknowledgedRequestIds: string[] = []
    for (const item of operations) {
      this.confirmedContent = applyOperation(this.confirmedContent, item.operation)
      const ownIndex = this.pending.findIndex((pending) => pending.requestId === item.request_id)
      if (ownIndex >= 0) {
        const [acknowledged] = this.pending.splice(ownIndex, 1)
        acknowledgedRequestIds.push(acknowledged.requestId)
        if (this.inFlight === acknowledged.requestId) this.inFlight = ''
      } else {
        this.pending = this.pending.map((pending) => ({ ...pending, operation: transformOperation(pending.operation, item.operation) }))
      }
    }
    this.confirmedVersion = version
    await this.commitLocalState(acknowledgedRequestIds)
    state.setDocument(this.withPending(this.confirmedContent), version)
    this.updatePendingCount()
    if (this.pending.length === 0) this.scheduleCursor()
  }

  private async applyEdit(message: RealtimeMessage): Promise<void> {
    if (!message.operation) return
    const state = useCollaborationStore.getState()
    const own = message.request_id && this.pending.some((item) => item.requestId === message.request_id)
    this.confirmedContent = applyOperation(this.confirmedContent, message.operation)
    this.confirmedVersion = message.version ?? this.confirmedVersion
    if (!own) {
      this.pending = this.pending.map((pending) => ({ ...pending, operation: transformOperation(pending.operation, message.operation!) }))
    }
    const acknowledgedRequestIds: string[] = []
    if (own && message.request_id) {
      this.pending = this.pending.filter((item) => item.requestId !== message.request_id)
      acknowledgedRequestIds.push(message.request_id)
      this.inFlight = ''
    }
    await this.commitLocalState(acknowledgedRequestIds)
    this.updatePendingCount()
    state.setDocument(this.withPending(this.confirmedContent), this.confirmedVersion)
    if (own) {
      this.sendNext()
      if (this.pending.length === 0) this.scheduleCursor()
    }
  }

  private sendNext(): void {
    const state = useCollaborationStore.getState()
    if (this.inFlight || state.connection !== 'CONNECTED' || this.pending.length === 0) return
    const next = this.pending[0]
    this.inFlight = next.requestId
    this.send({ type: 'edit', request_id: next.requestId, doc_id: this.documentId, base_version: this.confirmedVersion, operation: next.operation })
  }

  private scheduleCursor(): void {
    if (useCollaborationStore.getState().connection !== 'CONNECTED' || this.cursorTimer !== null) return
    const remaining = cursorThrottleMs - (Date.now() - this.lastCursorSentAt)
    if (remaining <= 0) {
      this.sendCurrentCursor(false)
      return
    }
    this.cursorTimer = window.setTimeout(() => {
      this.cursorTimer = null
      this.sendCurrentCursor(false)
    }, remaining)
  }

  private sendCurrentCursor(heartbeat: boolean): void {
    if (!this.currentCursor || this.pending.length > 0 || this.localChangesQueued > 0 || useCollaborationStore.getState().connection !== 'CONNECTED') return
    if (!heartbeat && Date.now() - this.lastCursorSentAt < cursorThrottleMs) {
      this.scheduleCursor()
      return
    }
    this.cursorSequence += 1
    this.lastCursorSentAt = Date.now()
    this.send({
      type: 'cursor_update',
      doc_id: this.documentId,
      cursor: {
        ...this.currentCursor,
        version: this.confirmedVersion,
        sequence: this.cursorSequence,
      },
    })
  }

  private send(message: RealtimeMessage): void {
    if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(message))
  }

  private scheduleReconnect(): void {
    useCollaborationStore.getState().setConnection('RECONNECTING')
    if (!navigator.onLine || this.reconnectTimer !== null) return
    const delay = Math.min(10_000, 250 * (2 ** this.reconnectAttempt)) + Math.floor(Math.random() * 200)
    this.reconnectAttempt += 1
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      this.connect(true)
    }, delay)
  }

  private updatePendingCount(): void { useCollaborationStore.getState().setPendingCount(this.pending.length) }

  private withPending(content: string): string {
    return this.pending.reduce((current, pending) => applyOperation(current, pending.operation), content)
  }

  private async commitLocalState(acknowledgedRequestIds: string[] = []): Promise<void> {
    await commitRecoveryState({
      userId: this.userId,
      documentId: this.documentId,
      content: this.confirmedContent,
      version: this.confirmedVersion,
    }, this.pending, acknowledgedRequestIds)
  }

  private resetRedirectChain(): void {
    this.redirectCount = 0
    this.visitedEndpoints.clear()
  }

  private advanceSeed(): void {
    const seeds = configuredGatewayURLs()
    if (seeds.length > 1) this.seedIndex = (this.seedIndex + 1) % seeds.length
  }
}
