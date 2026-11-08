import type { PermissionRole } from './api'

export type OperationKind = 'insert' | 'delete'

export interface Operation {
  id?: string
  user_id?: string
  base_version?: number
  kind: OperationKind
  position: number
  text?: string
  length?: number
}

export interface VersionedOperation {
  version: number
  request_id?: string
  operation: Operation
}

export interface Snapshot {
  version: number
  content: string
}

export interface CursorState {
  user_id?: string
  username?: string
  anchor: number
  head: number
  version: number
  sequence: number
}

export type RealtimeMessageType =
  | 'auth'
  | 'auth_ok'
  | 'join'
  | 'joined'
  | 'edit'
  | 'edit_applied'
  | 'permission_changed'
  | 'resume'
  | 'sync'
  | 'resync_required'
  | 'owner_redirect'
  | 'presence_changed'
  | 'cursor_update'
  | 'cursor_changed'
  | 'cursor_removed'
  | 'error'

export interface RealtimeMessage {
  type: RealtimeMessageType
  request_id?: string
  doc_id?: string
  base_version?: number
  last_version?: number
  version?: number
  from_version?: number
  to_version?: number
  current_version?: number
  content?: string
  role?: PermissionRole
  operation?: Operation
  operations?: VersionedOperation[]
  snapshot?: Snapshot
  code?: string
  message?: string
  token?: string
  endpoint?: string
  user_ids?: string[]
  cursor?: CursorState
}

export type ConnectionState =
  | 'DISCONNECTED'
  | 'CONNECTING'
  | 'AUTHENTICATING'
  | 'SYNCING'
  | 'CONNECTED'
  | 'RECONNECTING'
  | 'ERROR'
