import type { Operation } from '../types/protocol'

export interface PendingEdit {
  requestId: string
  userId: string
  documentId: string
  operation: Operation
  createdAt: number
}

export interface DocumentRecoveryState {
  scope: string
  userId: string
  documentId: string
  content: string
  version: number
}

const databaseName = 'collab-editor'
const databaseVersion = 2
const pendingStoreName = 'pending-edits'
const documentStoreName = 'document-states'
const userDocumentIndex = 'user-document'

function scopeFor(userId: string, documentId: string): string {
  return `${userId}\u0000${documentId}`
}

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, databaseVersion)
    request.onupgradeneeded = () => {
      const database = request.result
      let pendingStore: IDBObjectStore
      if (!database.objectStoreNames.contains(pendingStoreName)) {
        pendingStore = database.createObjectStore(pendingStoreName, { keyPath: 'requestId' })
      } else {
        pendingStore = request.transaction!.objectStore(pendingStoreName)
      }
      if (!pendingStore.indexNames.contains(userDocumentIndex)) {
        pendingStore.createIndex(userDocumentIndex, ['userId', 'documentId'])
      }
      if (!database.objectStoreNames.contains(documentStoreName)) {
        database.createObjectStore(documentStoreName, { keyPath: 'scope' })
      }
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('Unable to open pending edit storage.'))
  })
}

function transactionComplete(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve()
    transaction.onerror = () => reject(transaction.error ?? new Error('IndexedDB transaction failed.'))
    transaction.onabort = () => reject(transaction.error ?? new Error('IndexedDB transaction was aborted.'))
  })
}

export async function savePending(edit: PendingEdit): Promise<void> {
  const database = await openDatabase()
  try {
    const transaction = database.transaction(pendingStoreName, 'readwrite')
    transaction.objectStore(pendingStoreName).put(edit)
    await transactionComplete(transaction)
  } finally {
    database.close()
  }
}

export async function removePending(requestId: string): Promise<void> {
  const database = await openDatabase()
  try {
    const transaction = database.transaction(pendingStoreName, 'readwrite')
    transaction.objectStore(pendingStoreName).delete(requestId)
    await transactionComplete(transaction)
  } finally {
    database.close()
  }
}

export async function loadPending(userId: string, documentId: string): Promise<PendingEdit[]> {
  const database = await openDatabase()
  try {
    return await new Promise((resolve, reject) => {
      const transaction = database.transaction(pendingStoreName, 'readonly')
      const request = transaction.objectStore(pendingStoreName).index(userDocumentIndex).getAll([userId, documentId])
      request.onsuccess = () => resolve((request.result as PendingEdit[]).sort((a, b) => a.createdAt - b.createdAt))
      request.onerror = () => reject(request.error ?? new Error('Unable to read pending edits.'))
    })
  } finally {
    database.close()
  }
}

export async function loadRecoveryState(userId: string, documentId: string): Promise<DocumentRecoveryState | null> {
  const database = await openDatabase()
  try {
    return await new Promise((resolve, reject) => {
      const transaction = database.transaction(documentStoreName, 'readonly')
      const request = transaction.objectStore(documentStoreName).get(scopeFor(userId, documentId))
      request.onsuccess = () => resolve((request.result as DocumentRecoveryState | undefined) ?? null)
      request.onerror = () => reject(request.error ?? new Error('Unable to read the recovery state.'))
    })
  } finally {
    database.close()
  }
}

export async function commitRecoveryState(
  state: Omit<DocumentRecoveryState, 'scope'>,
  pending: PendingEdit[],
  acknowledgedRequestIds: string[] = [],
): Promise<void> {
  const database = await openDatabase()
  try {
    const transaction = database.transaction([pendingStoreName, documentStoreName], 'readwrite')
    const pendingStore = transaction.objectStore(pendingStoreName)
    for (const requestId of acknowledgedRequestIds) pendingStore.delete(requestId)
    for (const edit of pending) pendingStore.put(edit)
    transaction.objectStore(documentStoreName).put({ ...state, scope: scopeFor(state.userId, state.documentId) })
    await transactionComplete(transaction)
  } finally {
    database.close()
  }
}

export function recoveryScope(userId: string, documentId: string): string {
  return scopeFor(userId, documentId)
}
