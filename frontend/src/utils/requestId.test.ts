import { describe, expect, it } from 'vitest'
import { createRequestId } from './requestId'

describe('createRequestId', () => {
  it('creates unique protocol-safe identifiers', () => {
    const first = createRequestId('edit')
    const second = createRequestId('edit')

    expect(first).toMatch(/^edit-[0-9a-f-]{36}$/)
    expect(second).toMatch(/^edit-[0-9a-f-]{36}$/)
    expect(first).not.toBe(second)
  })
})
