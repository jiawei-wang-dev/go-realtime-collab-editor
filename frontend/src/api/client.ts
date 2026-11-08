import axios, { AxiosError } from 'axios'
import { ApiError, type ApiErrorBody } from '../types/api'
import type { AccessTokenResponse } from '../types/api'
import { createRequestId } from '../utils/requestId'

let accessToken = ''
let refreshInFlight: Promise<string> | null = null

export const apiClient = axios.create({
  baseURL: import.meta.env.VITE_API_URL ?? '',
  timeout: 10_000,
  withCredentials: true,
  headers: { 'Content-Type': 'application/json' },
})

apiClient.interceptors.request.use((config) => {
  if (accessToken) {
    config.headers.Authorization = `Bearer ${accessToken}`
  }
  return config
})

apiClient.interceptors.response.use(
  (response) => response,
  async (error: AxiosError<ApiErrorBody>) => {
    const request = error.config as (typeof error.config & { _retried?: boolean })
    const isBrowserAuth = request?.url?.includes('/api/browser/auth/')
    if (error.response?.status === 401 && request && !request._retried && !isBrowserAuth) {
      request._retried = true
      refreshInFlight ??= axios.post<AccessTokenResponse>(
        '/api/browser/auth/refresh',
        { request_id: createRequestId('http-refresh') },
        { baseURL: apiClient.defaults.baseURL, timeout: 10_000, withCredentials: true },
      ).then((response) => {
        setAccessToken(response.data.access_token)
        return response.data.access_token
      }).finally(() => { refreshInFlight = null })
      try {
        const token = await refreshInFlight
        request.headers = request.headers ?? {}
        request.headers.Authorization = `Bearer ${token}`
        return apiClient(request)
      } catch {
        clearAccessToken()
        window.dispatchEvent(new Event('collab:session-expired'))
      }
    }
    const status = error.response?.status ?? 0
    const body = error.response?.data
    throw new ApiError(
      status,
      body?.code ?? 'NETWORK_ERROR',
      body?.message ?? (status === 0 ? 'Unable to reach the server.' : 'The request failed.'),
    )
  },
)

export function setAccessToken(token: string): void {
  accessToken = token
}

export function clearAccessToken(): void {
  accessToken = ''
}

export function getAccessToken(): string { return accessToken }
