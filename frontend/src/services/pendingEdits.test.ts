import 'fake-indexeddb/auto'
import { describe, expect, it } from 'vitest'
import { commitRecoveryState, loadPending, loadRecoveryState, removePending, savePending } from './pendingEdits'

describe('IndexedDB pending edit persistence', () => {
  it('restores edits in creation order and removes acknowledged requests', async () => {
    const documentId = `doc-${crypto.randomUUID()}`
    const userId = `user-${crypto.randomUUID()}`
    await savePending({ requestId: 'second', userId, documentId, operation: { kind: 'insert', position: 1, text: 'B' }, createdAt: 2 })
    await savePending({ requestId: 'first', userId, documentId, operation: { kind: 'insert', position: 0, text: 'A' }, createdAt: 1 })
    expect((await loadPending(userId, documentId)).map((edit) => edit.requestId)).toEqual(['first', 'second'])
    await removePending('first')
    expect((await loadPending(userId, documentId)).map((edit) => edit.requestId)).toEqual(['second'])
    await removePending('second')
  })

  it('isolates users and atomically acknowledges pending edits with the confirmed version', async () => {
    const documentId = `shared-${crypto.randomUUID()}`
    const userA = `a-${crypto.randomUUID()}`
    const userB = `b-${crypto.randomUUID()}`
    const editA = { requestId: crypto.randomUUID(), userId: userA, documentId, operation: { kind: 'insert' as const, position: 0, text: 'A' }, createdAt: 1 }
    const editB = { requestId: crypto.randomUUID(), userId: userB, documentId, operation: { kind: 'insert' as const, position: 0, text: 'B' }, createdAt: 1 }
    await savePending(editA)
    await savePending(editB)

    await commitRecoveryState({ userId: userA, documentId, content: 'A', version: 1 }, [], [editA.requestId])

    expect(await loadPending(userA, documentId)).toEqual([])
    expect((await loadPending(userB, documentId)).map((edit) => edit.requestId)).toEqual([editB.requestId])
    expect(await loadRecoveryState(userA, documentId)).toMatchObject({ content: 'A', version: 1 })
    await removePending(editB.requestId)
  })
})
