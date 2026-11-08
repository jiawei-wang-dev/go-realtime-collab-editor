import type { Monaco } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'
import type { CursorState } from '../types/protocol'

const remoteCursorColors = 8
const remoteCursorExpiryMs = 30_000

interface VisibleCursor extends CursorState {
  user_id: string
  username: string
  lastSeen: number
}

export function utf16OffsetToCodePoint(text: string, offset: number): number {
  return Array.from(text.slice(0, offset)).length
}

export function codePointOffsetToUTF16(text: string, offset: number): number {
  return Array.from(text).slice(0, offset).join('').length
}

export function remoteCursorColor(userID: string): number {
  let hash = 2166136261
  for (const character of userID) {
    hash ^= character.codePointAt(0) ?? 0
    hash = Math.imul(hash, 16777619)
  }
  return Math.abs(hash) % remoteCursorColors
}

export class RemoteCursorDecorations {
  private readonly cursors = new Map<string, VisibleCursor>()
  private readonly decorations: editor.IEditorDecorationsCollection
  private readonly expiryTimer: number

  constructor(
    private readonly editor: editor.IStandaloneCodeEditor,
    private readonly monaco: Monaco,
  ) {
    this.decorations = editor.createDecorationsCollection()
    this.expiryTimer = window.setInterval(() => this.removeExpired(), 5_000)
  }

  update(cursor: CursorState): void {
    if (!cursor.user_id || !cursor.username) return
    this.cursors.set(cursor.user_id, {
      ...cursor,
      user_id: cursor.user_id,
      username: cursor.username,
      lastSeen: Date.now(),
    })
    this.render()
  }

  remove(userID: string): void {
    if (this.cursors.delete(userID)) this.render()
  }

  dispose(): void {
    window.clearInterval(this.expiryTimer)
    this.cursors.clear()
    this.decorations.clear()
  }

  private removeExpired(): void {
    const expiredBefore = Date.now() - remoteCursorExpiryMs
    let changed = false
    for (const [userID, cursor] of this.cursors) {
      if (cursor.lastSeen < expiredBefore) {
        this.cursors.delete(userID)
        changed = true
      }
    }
    if (changed) this.render()
  }

  private render(): void {
    const model = this.editor.getModel()
    if (!model) {
      this.decorations.clear()
      return
    }
    const content = model.getValue()
    const codePointLength = Array.from(content).length
    const decorations: editor.IModelDeltaDecoration[] = []
    for (const cursor of this.cursors.values()) {
      if (cursor.anchor > codePointLength || cursor.head > codePointLength) continue
      const anchor = model.getPositionAt(codePointOffsetToUTF16(content, cursor.anchor))
      const head = model.getPositionAt(codePointOffsetToUTF16(content, cursor.head))
      const start = this.monaco.Position.isBeforeOrEqual(anchor, head) ? anchor : head
      const end = this.monaco.Position.isBeforeOrEqual(anchor, head) ? head : anchor
      const color = remoteCursorColor(cursor.user_id)
      if (!this.monaco.Position.equals(start, end)) {
        decorations.push({
          range: new this.monaco.Range(start.lineNumber, start.column, end.lineNumber, end.column),
          options: { className: `remote-selection remote-color-${color}` },
        })
      }
      decorations.push({
        range: new this.monaco.Range(head.lineNumber, head.column, head.lineNumber, head.column),
        options: {
          className: `remote-cursor-caret remote-color-${color}`,
          showIfCollapsed: true,
          zIndex: 20,
          after: {
            content: cursor.username,
            inlineClassName: `remote-cursor-label remote-label-${color}`,
            inlineClassNameAffectsLetterSpacing: true,
          },
        },
      })
    }
    this.decorations.set(decorations)
  }
}
