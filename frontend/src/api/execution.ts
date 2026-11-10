import { apiClient } from './client'
import type { RunResult } from '../types/api'

export async function runGoDocument(documentId: string, source: string): Promise<RunResult> {
  const response = await apiClient.post<RunResult>(
    `/api/v1/documents/${encodeURIComponent(documentId)}/run`,
    { language: 'go', source },
    { timeout: 15_000 },
  )
  return response.data
}
