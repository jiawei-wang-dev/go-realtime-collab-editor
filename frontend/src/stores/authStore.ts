import { create } from 'zustand'
import * as authApi from '../api/auth'
import { clearAccessToken, setAccessToken } from '../api/client'
import type { User } from '../types/api'
import { createRequestId } from '../utils/requestId'
import { useCollaborationStore } from './collaborationStore'
import { useDocumentStore } from './documentStore'

type AuthPhase = 'BOOTSTRAPPING' | 'ANONYMOUS' | 'AUTHENTICATED'

interface AuthState {
  phase: AuthPhase
  user: User | null
  accessToken: string
  expiresAt: number
  loading: boolean
  error: string
  bootstrap: () => Promise<void>
  login: (username: string, password: string) => Promise<void>
  register: (username: string, password: string) => Promise<void>
  refresh: () => Promise<string>
  logout: () => Promise<void>
  expire: () => void
  clearError: () => void
}

function applyToken(token: string, expiresIn: number): Pick<AuthState, 'accessToken' | 'expiresAt'> {
  setAccessToken(token)
  return { accessToken: token, expiresAt: Date.now() + expiresIn * 1000 }
}

async function rotateBrowserSession(prefix: string) {
  const rotate = () => authApi.refresh(createRequestId(prefix))
  if (navigator.locks) return navigator.locks.request('collab-auth-refresh', rotate)
  return rotate()
}

function clearSensitiveState(): void {
  clearAccessToken()
  useDocumentStore.getState().reset()
  useCollaborationStore.getState().reset()
}

export const useAuthStore = create<AuthState>((set, get) => ({
  phase: 'BOOTSTRAPPING',
  user: null,
  accessToken: '',
  expiresAt: 0,
  loading: false,
  error: '',

  bootstrap: async () => {
    set({ phase: 'BOOTSTRAPPING', error: '' })
    try {
      const token = await rotateBrowserSession('bootstrap')
      const tokenState = applyToken(token.access_token, token.expires_in)
      const user = await authApi.getCurrentUser()
      set({ ...tokenState, user, phase: 'AUTHENTICATED' })
    } catch {
      clearSensitiveState()
      set({ accessToken: '', expiresAt: 0, user: null, phase: 'ANONYMOUS' })
    }
  },

  login: async (username, password) => {
    set({ loading: true, error: '' })
    try {
      const token = await authApi.login(username, password)
      const tokenState = applyToken(token.access_token, token.expires_in)
      const user = await authApi.getCurrentUser()
      set({ ...tokenState, user, phase: 'AUTHENTICATED', loading: false })
    } catch (error) {
      set({ loading: false, error: error instanceof Error ? error.message : 'Login failed.' })
      throw error
    }
  },

  register: async (username, password) => {
    set({ loading: true, error: '' })
    try {
      await authApi.register(username, password)
      await get().login(username, password)
    } catch (error) {
      set({ loading: false, error: error instanceof Error ? error.message : 'Registration failed.' })
      throw error
    }
  },

  refresh: async () => {
    const token = await rotateBrowserSession('refresh')
    const tokenState = applyToken(token.access_token, token.expires_in)
    set(tokenState)
    return token.access_token
  },

  logout: async () => {
    set({ loading: true, error: '' })
    try {
      await authApi.logout()
    } catch {
      // Local credentials are cleared even when the network cannot confirm logout.
    } finally {
      clearSensitiveState()
      set({ phase: 'ANONYMOUS', user: null, accessToken: '', expiresAt: 0, loading: false })
      authChannel?.postMessage('logout')
    }
  },

  expire: () => {
    clearSensitiveState()
    set({ phase: 'ANONYMOUS', user: null, accessToken: '', expiresAt: 0, loading: false, error: 'Your session has expired. Please sign in again.' })
  },

  clearError: () => set({ error: '' }),
}))

const authChannel = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel('collab-auth')
if (authChannel) {
  authChannel.onmessage = (event) => {
    if (event.data === 'logout') useAuthStore.getState().expire()
  }
}
