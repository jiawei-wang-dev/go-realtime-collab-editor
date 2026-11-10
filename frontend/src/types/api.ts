export type UserStatus = 'active' | 'disabled'

export interface User {
  id: string
  username: string
  status: UserStatus
  created_at?: string
  updated_at?: string
}

export interface AccessTokenResponse {
  access_token: string
  token_type: 'Bearer'
  expires_in: number
}

export interface DocumentRecord {
  id: string
  content: string
  version: number
}

export type PermissionRole = 'owner' | 'write' | 'read'

export interface DocumentSummary {
  id: string
  version: number
  role: PermissionRole
  updated_at: string
}

export interface DocumentPermission {
  doc_id: string
  user_id: string
  username?: string
  role: PermissionRole
}

export interface RunResult {
  stdout: string
  stderr: string
  exit_code: number
  duration_ms: number
  timed_out: boolean
  output_truncated: boolean
}

export interface ApiErrorBody {
  type: 'error'
  code: string
  message: string
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}
