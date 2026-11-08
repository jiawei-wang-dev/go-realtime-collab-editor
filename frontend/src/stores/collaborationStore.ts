import { create } from 'zustand'
import type { ConnectionState } from '../types/protocol'
import type { PermissionRole } from '../types/api'

interface CollaborationState {
  loading: boolean
  documentId: string
  content: string
  version: number
  role: PermissionRole | ''
  connection: ConnectionState
  synchronized: boolean
  participants: string[]
  pendingCount: number
  error: string
  beginLoad: (documentId: string) => void
  initialize: (documentId: string, content: string, version: number) => void
  setConnection: (connection: ConnectionState) => void
  setDocument: (content: string, version: number) => void
  setRole: (role: PermissionRole) => void
  setParticipants: (participants: string[]) => void
  setPendingCount: (pendingCount: number) => void
  setError: (error: string) => void
  reset: () => void
}

const initialState = {
  loading: false,
  documentId: '',
  content: '',
  version: 0,
  role: '' as const,
  connection: 'DISCONNECTED' as const,
  synchronized: false,
  participants: [] as string[],
  pendingCount: 0,
  error: '',
}

export const useCollaborationStore = create<CollaborationState>((set) => ({
  ...initialState,
  beginLoad: (documentId) => set({ ...initialState, documentId, loading: true }),
  initialize: (documentId, content, version) => set({ documentId, content, version, synchronized: true, error: '', loading: false }),
  setConnection: (connection) => set({ connection, synchronized: connection === 'CONNECTED' }),
  setDocument: (content, version) => set({ content, version }),
  setRole: (role) => set({ role }),
  setParticipants: (participants) => set({ participants }),
  setPendingCount: (pendingCount) => set({ pendingCount }),
  setError: (error) => set({ error, connection: 'ERROR', synchronized: false, loading: false }),
  reset: () => set(initialState),
}))
