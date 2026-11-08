import { describe, expect, it } from 'vitest'
import { codePointOffsetToUTF16, remoteCursorColor, utf16OffsetToCodePoint } from './remoteCursors'

describe('remote cursor offsets', () => {
  it('converts Monaco UTF-16 offsets to protocol code-point offsets', () => {
    const content = 'a😀文b'
    expect(utf16OffsetToCodePoint(content, 0)).toBe(0)
    expect(utf16OffsetToCodePoint(content, 1)).toBe(1)
    expect(utf16OffsetToCodePoint(content, 3)).toBe(2)
    expect(utf16OffsetToCodePoint(content, content.length)).toBe(4)
  })

  it('converts protocol code-point offsets to Monaco UTF-16 offsets', () => {
    const content = 'a😀文b'
    expect(codePointOffsetToUTF16(content, 0)).toBe(0)
    expect(codePointOffsetToUTF16(content, 1)).toBe(1)
    expect(codePointOffsetToUTF16(content, 2)).toBe(3)
    expect(codePointOffsetToUTF16(content, 4)).toBe(content.length)
  })

  it('assigns a stable palette entry per user', () => {
    expect(remoteCursorColor('user-a')).toBe(remoteCursorColor('user-a'))
    expect(remoteCursorColor('user-a')).toBeGreaterThanOrEqual(0)
    expect(remoteCursorColor('user-a')).toBeLessThan(8)
    expect(remoteCursorColor('user-a')).not.toBe(remoteCursorColor('user-b'))
  })
})
