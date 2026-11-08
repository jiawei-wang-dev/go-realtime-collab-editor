import { apiClient } from './client'
import type { DocumentPermission, DocumentRecord, DocumentSummary, PermissionRole, User } from '../types/api'

export async function listDocuments(): Promise<DocumentSummary[]> {
  const response = await apiClient.get<{ documents: DocumentSummary[] }>('/api/documents')
  return response.data.documents
}

export async function createDocument(id: string, content = ''): Promise<DocumentRecord> {
  const response = await apiClient.post<DocumentRecord>('/api/documents', { id, content })
  return response.data
}

export async function getDocument(id: string): Promise<DocumentRecord> {
  const response = await apiClient.get<DocumentRecord>(`/api/documents/${encodeURIComponent(id)}`)
  return response.data
}

export async function deleteDocument(id: string): Promise<void> {
  await apiClient.delete(`/api/documents/${encodeURIComponent(id)}`)
}

export async function listPermissions(id: string): Promise<DocumentPermission[]> {
  const response = await apiClient.get<{ permissions: DocumentPermission[] }>(
    `/api/documents/${encodeURIComponent(id)}/permissions`,
  )
  return response.data.permissions
}

export async function updatePermission(id: string, userId: string, role: PermissionRole): Promise<DocumentPermission> {
  const response = await apiClient.put<DocumentPermission>(
    `/api/documents/${encodeURIComponent(id)}/permissions/${encodeURIComponent(userId)}`,
    { role },
  )
  return response.data
}

export async function lookupUser(username: string): Promise<User> {
  const response = await apiClient.get<User>('/api/users/lookup', { params: { username } })
  return response.data
}
