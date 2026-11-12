import { useAuthStore } from '../stores/authStore'
import { getAccessToken } from '../api/client'
import { useCollaborationStore } from '../stores/collaborationStore'
import type { CursorState, RealtimeMessage } from '../types/protocol'
import { createRequestId } from '../utils/requestId'
import { applyOperation, diffOperations, transformOperation } from './operations'
import { commitRecoveryState, loadPending, loadRecoveryState, savePending, type PendingEdit } from './pendingEdits'
import { connectRealtimeTransport, realtimeSeedCount, websocketURL, type RealtimeTransport } from './realtimeTransport'

const maxRedirects = 5
const cursorThrottleMs = 50
const cursorHeartbeatMs = 10_000

export interface RealtimeSessionObserver {
  cursorChanged?: (cursor: CursorState) => void
  cursorRemoved?: (userID: string) => void
}

export class RealtimeSession {
  private transport: RealtimeTransport | null = null

  private connectionGeneration = 0
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
  private tasks: Promise<void> = Promise.resolve()
  private reconnectTimer: number | null = null
  private cursorTimer: number | null = null
  private cursorHeartbeat: number | null = null
  private cursorSequence = 0
  private lastCursorSentAt = 0
  private currentCursor: Pick<CursorState, 'anchor' | 'head'> | null = null
  private localChangesQueued = 0
  private localSaveError: Error | undefined

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
    if (this.stopped) return
    this.pending = pending
    const state = useCollaborationStore.getState()
    this.confirmedContent = recoveryState?.content ?? state.content
    this.confirmedVersion = recoveryState?.version ?? state.version
    this.hasJoined = recoveryState !== null
    state.setDocument(this.withPending(this.confirmedContent), this.confirmedVersion)
    this.updatePendingCount()
    this.cursorHeartbeat = window.setInterval(() => this.sendCurrentCursor(true), cursorHeartbeatMs)
    void this.connect(false)
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
    this.connectionGeneration += 1
    this.transport?.close('editor closed')
    this.transport = null
  }

  private readonly handleOffline = (): void => {
    if (this.stopped) return
    useCollaborationStore.getState().setConnection('RECONNECTING')
    const transport = this.transport
    this.transport = null
    this.inFlight = ''
    transport?.close('browser offline')
  }

  private readonly handleOnline = (): void => {
    if (this.stopped || this.transport !== null || this.reconnectTimer !== null) return
    void this.connect(true)
  }

  async updateContent(next: string): Promise<void> {
    this.localChangesQueued += 1
    const update = this.tasks
      .catch(() => undefined)
      .then(async () => {
        await this.persistLocalChange(next)
        this.localSaveError = undefined
      })
      .finally(() => {
        this.localChangesQueued -= 1
        if (this.localChangesQueued === 0) this.scheduleCursor()
      })
    this.tasks = update.catch((error: unknown) => {
      this.localSaveError = error instanceof Error ? error : new Error('Unable to save the local edit.')
      useCollaborationStore.getState().setError(this.localSaveError.message)
    })
    return update
  }

  async flushEdits(): Promise<void> {
    let queued: Promise<void>
    do {
      queued = this.tasks
      await queued
    } while (queued !== this.tasks)
    if (this.localSaveError) throw this.localSaveError
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

  private async connect(reconnecting: boolean): Promise<void> {
    if (this.stopped) return
    useCollaborationStore.getState().setConnection(reconnecting ? 'RECONNECTING' : 'CONNECTING')
    const generation = ++this.connectionGeneration
    try {
      const transport = await connectRealtimeTransport(this.endpoint || undefined, this.seedIndex, {
        message: (message) => this.handleMessage(message, generation),
        close: () => this.handleTransportClose(generation),
      })
      if (this.stopped || generation !== this.connectionGeneration) {
        transport.close('superseded connection')
        return
      }
      this.transport = transport
      useCollaborationStore.getState().setTransport(transport.kind)
      this.reconnectAttempt = 0
      useCollaborationStore.getState().setConnection('AUTHENTICATING')
      transport.send({ type: 'auth', token: getAccessToken() })
    } catch (error) {
      if (this.stopped || generation !== this.connectionGeneration) return
      if (error instanceof Error && (error.message.includes('invalid endpoint') || error.message.includes('insecure endpoint'))) {
        useCollaborationStore.getState().setError(error.message)
        this.stop()
        return
      }
      if (this.endpoint !== '') {
        this.endpoint = ''
        this.redirectCount = 0
        this.visitedEndpoints.clear()
      } else this.advanceSeed()
      this.scheduleReconnect()
    }
  }

  private handleMessage(message: RealtimeMessage, generation: number): void {
    if (generation !== this.connectionGeneration) return
    this.tasks = this.tasks.catch(() => undefined).then(async () => {
      if (!this.stopped && generation === this.connectionGeneration) await this.receive(message)
    }).catch((error: unknown) => {
      useCollaborationStore.getState().setError(error instanceof Error ? error.message : 'Realtime recovery failed.')
      this.disconnectAndReconnect('recovery failed')
    })
  }

  private handleTransportClose(generation: number): void {
    if (generation !== this.connectionGeneration) return
    this.transport = null
    this.inFlight = ''
    if (this.endpoint !== '') {
      this.endpoint = ''
      this.redirectCount = 0
      this.visitedEndpoints.clear()
    } else this.advanceSeed()
    if (!this.stopped) this.scheduleReconnect()
  }

  private disconnectAndReconnect(reason: string): void {
    const transport = this.transport
    this.transport = null
    this.inFlight = ''
    this.connectionGeneration += 1
    transport?.close(reason)
    if (!this.stopped) void this.connect(true)
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
          const redirected = websocketURL(message.endpoint)
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
        this.disconnectAndReconnect('following owner redirect')
        break
      case 'error':
        if (message.code === 'RESYNC_REQUIRED') { this.send({ type: 'resume', doc_id: this.documentId, last_version: 0 }); return }
        if (message.code === 'PERMISSION_DENIED' && state.connection !== 'SYNCING' && state.connection !== 'CONNECTED' && !this.authRefreshAttempted) {
          this.authRefreshAttempted = true
          void useAuthStore.getState().refresh()
            .then(() => this.disconnectAndReconnect('token refreshed'))
            .catch(() => {
              state.setError('Your session is no longer active.')
              this.stop()
            })
          return
        }
        state.setError(message.code === 'PERMISSION_DENIED' && state.connection === 'SYNCING'
          ? 'You no longer have access to this file.'
          : message.message ?? 'Realtime collaboration failed.')
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
    this.transport?.send(message)
  }

  private scheduleReconnect(): void {
    useCollaborationStore.getState().setConnection('RECONNECTING')
    if (!navigator.onLine || this.reconnectTimer !== null) return
    const delay = Math.min(10_000, 250 * (2 ** this.reconnectAttempt)) + Math.floor(Math.random() * 200)
    this.reconnectAttempt += 1
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      void this.connect(true)
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
    const seedCount = realtimeSeedCount()
    if (seedCount > 1) this.seedIndex = (this.seedIndex + 1) % seedCount
  }
}
