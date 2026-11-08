import { apiClient } from './client'
import type { AccessTokenResponse, User } from '../types/api'

export async function register(username: string, password: string): Promise<User> {
  const response = await apiClient.post<User>('/api/auth/register', { username, password })
  return response.data
}

export async function login(username: string, password: string): Promise<AccessTokenResponse> {
  const response = await apiClient.post<AccessTokenResponse>('/api/browser/auth/login', { username, password })
  return response.data
}

export async function refresh(requestId: string): Promise<AccessTokenResponse> {
  const response = await apiClient.post<AccessTokenResponse>('/api/browser/auth/refresh', { request_id: requestId })
  return response.data
}

export async function logout(): Promise<void> {
  await apiClient.post('/api/browser/auth/logout')
}

export async function getCurrentUser(): Promise<User> {
  const response = await apiClient.get<User>('/api/auth/me')
  return response.data
}
