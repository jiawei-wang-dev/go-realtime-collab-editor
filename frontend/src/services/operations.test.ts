import { describe, expect, it } from 'vitest'
import { applyOperation, diffOperations, transformOperation } from './operations'

describe('Unicode-safe editor operations', () => {
  it('uses Unicode code point offsets compatible with the Go backend', () => {
    expect(applyOperation('A😀B', { kind: 'insert', position: 2, text: '中' })).toBe('A😀中B')
    expect(applyOperation('A😀中B', { kind: 'delete', position: 1, length: 2 })).toBe('AB')
  })

  it('builds a minimal replacement as delete then insert', () => {
    expect(diffOperations('hello 世界', 'hello Go')).toEqual([
      { kind: 'delete', position: 6, length: 2 },
      { kind: 'insert', position: 6, text: 'Go' },
    ])
  })

	it('transforms a pending Unicode insert over a remote insert', () => {
		expect(transformOperation({ kind: 'insert', position: 1, text: 'X' }, { kind: 'insert', position: 0, text: '😀' })).toEqual({ kind: 'insert', position: 2, text: 'X' })
	})
})
