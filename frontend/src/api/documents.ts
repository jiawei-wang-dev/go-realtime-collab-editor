import { apiClient } from './client'
import type { DocumentPermission, DocumentRecord, DocumentSummary, Folder, PermissionRole, User } from '../types/api'

export async function listDocuments(): Promise<DocumentSummary[]> {
  const response = await apiClient.get<{ documents: DocumentSummary[] }>('/api/documents')
  return response.data.documents
}

export async function createDocument(id: string, content = '', folderId?: string): Promise<DocumentRecord> {
  const response = await apiClient.post<DocumentRecord>('/api/documents', { id, content, folder_id: folderId })
  return response.data
}

export async function listFolders(): Promise<Folder[]> {
  const response = await apiClient.get<{ folders: Folder[] }>('/api/folders')
  return response.data.folders
}

export async function createFolder(name: string, parentId?: string): Promise<Folder> {
  const response = await apiClient.post<Folder>('/api/folders', { name, parent_id: parentId })
  return response.data
}

export async function renameFolder(id: string, name: string): Promise<void> {
  await apiClient.put(`/api/folders/${encodeURIComponent(id)}/name`, { name })
}

export async function shareFolder(id: string, username: string, role: 'read' | 'write' | 'revoke'): Promise<void> {
  const user = await lookupUser(username)
  await changeFolderAccess(id, user.id, role)
}

export interface FolderAccess {
  user_id: string
  username: string
  role: PermissionRole
  folder_id: string
  folder_name: string
}

export async function listFolderAccess(id: string): Promise<FolderAccess[]> {
  const response = await apiClient.get<FolderAccess[]>(`/api/folders/${encodeURIComponent(id)}/permissions`)
  return response.data
}

export async function listDocumentAccess(id: string): Promise<FolderAccess[]> {
  const response = await apiClient.get<FolderAccess[]>(`/api/documents/${encodeURIComponent(id)}/access`)
  return response.data
}

export async function changeDocumentAccess(id: string, userId: string, role: 'read' | 'write' | 'revoke'): Promise<void> {
  if (role === 'revoke') {
    await apiClient.delete(`/api/documents/${encodeURIComponent(id)}/permissions/${encodeURIComponent(userId)}`)
  } else {
    await updatePermission(id, userId, role)
  }
}

export async function changeFolderAccess(id: string, userId: string, role: 'read' | 'write' | 'revoke'): Promise<void> {
  const path = `/api/folders/${encodeURIComponent(id)}/permissions/${encodeURIComponent(userId)}`
  if (role === 'revoke') {
    await apiClient.delete(path)
  } else {
    await apiClient.put(path, { role })
  }
}

export async function moveDocument(id: string, folderId: string): Promise<void> {
  await apiClient.put(`/api/documents/${encodeURIComponent(id)}/folder`, { folder_id: folderId })
}

export interface AccessChange {
  user_id: string
  username?: string
  before: PermissionRole | ''
  after: PermissionRole | ''
}

export async function previewFolderMove(id: string, parentId: string): Promise<AccessChange[]> {
  const response = await apiClient.get<AccessChange[]>(`/api/folders/${encodeURIComponent(id)}/parent`, { params: { parent_id: parentId } })
  return response.data
}

export async function moveFolder(id: string, parentId: string): Promise<void> {
  await apiClient.put(`/api/folders/${encodeURIComponent(id)}/parent`, { parent_id: parentId })
}

export async function deleteEmptyFolder(id: string): Promise<void> {
  await apiClient.delete(`/api/folders/${encodeURIComponent(id)}`)
}

export async function previewDocumentMove(id: string, folderId: string): Promise<AccessChange[]> {
  const response = await apiClient.get<AccessChange[]>(`/api/documents/${encodeURIComponent(id)}/folder`, { params: { folder_id: folderId } })
  return response.data
}

export async function getDocument(id: string): Promise<DocumentRecord> {
  const response = await apiClient.get<DocumentRecord>(`/api/documents/${encodeURIComponent(id)}`)
  return response.data
}

export interface HistoryEntry {
  version: number
  user_id?: string
  kind?: 'insert' | 'delete'
}

export async function listHistory(id: string, before?: number): Promise<HistoryEntry[]> {
  const response = await apiClient.get<HistoryEntry[]>(`/api/documents/${encodeURIComponent(id)}/history`, { params: { before } })
  return response.data
}

export async function getHistoricalVersion(id: string, version: number): Promise<DocumentRecord> {
  const response = await apiClient.get<DocumentRecord>(`/api/documents/${encodeURIComponent(id)}/history`, { params: { version } })
  return response.data
}

export async function renameDocument(id: string, name: string): Promise<void> {
  await apiClient.put(`/api/documents/${encodeURIComponent(id)}/name`, { name })
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
