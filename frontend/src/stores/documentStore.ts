import { create } from 'zustand'
import * as documentsApi from '../api/documents'
import type { DocumentSummary } from '../types/api'

interface DocumentState {
  documents: DocumentSummary[]
  loading: boolean
  error: string
  load: () => Promise<void>
  create: (id: string) => Promise<void>
  remove: (id: string) => Promise<void>
  clearError: () => void
  reset: () => void
}

export const useDocumentStore = create<DocumentState>((set) => ({
  documents: [],
  loading: false,
  error: '',

  load: async () => {
    set({ loading: true, error: '' })
    try {
      const documents = await documentsApi.listDocuments()
      set({ documents, loading: false })
    } catch (error) {
      set({ loading: false, error: error instanceof Error ? error.message : 'Unable to load documents.' })
    }
  },

  create: async (id) => {
    set({ loading: true, error: '' })
    try {
      await documentsApi.createDocument(id)
      const documents = await documentsApi.listDocuments()
      set({ documents, loading: false })
    } catch (error) {
      set({ loading: false, error: error instanceof Error ? error.message : 'Unable to create the document.' })
      throw error
    }
  },

  remove: async (id) => {
    set({ loading: true, error: '' })
    try {
      await documentsApi.deleteDocument(id)
      set((state) => ({ documents: state.documents.filter((document) => document.id !== id), loading: false }))
    } catch (error) {
      set({ loading: false, error: error instanceof Error ? error.message : 'Unable to delete the document.' })
      throw error
    }
  },

  clearError: () => set({ error: '' }),
  reset: () => set({ documents: [], loading: false, error: '' }),
}))
