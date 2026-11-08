import type { Operation } from '../types/protocol'

export function codePoints(value: string): string[] { return Array.from(value) }

export function applyOperation(content: string, operation: Operation): string {
  const source = codePoints(content)
  if (operation.position < 0 || operation.position > source.length) throw new Error('Operation position is outside the document.')
  if (operation.kind === 'insert') {
    const inserted = codePoints(operation.text ?? '')
    if (inserted.length === 0) throw new Error('Insert text must not be empty.')
    return [...source.slice(0, operation.position), ...inserted, ...source.slice(operation.position)].join('')
  }
  const length = operation.length ?? 0
  if (length < 0 || operation.position + length > source.length) throw new Error('Delete range is outside the document.')
  return [...source.slice(0, operation.position), ...source.slice(operation.position + length)].join('')
}

export function diffOperations(previous: string, next: string): Operation[] {
  const before = codePoints(previous)
  const after = codePoints(next)
  let prefix = 0
  while (prefix < before.length && prefix < after.length && before[prefix] === after[prefix]) prefix += 1
  let suffix = 0
  while (suffix < before.length - prefix && suffix < after.length - prefix && before[before.length - 1 - suffix] === after[after.length - 1 - suffix]) suffix += 1
  const removed = before.length - prefix - suffix
  const inserted = after.slice(prefix, after.length - suffix).join('')
  const operations: Operation[] = []
  if (removed > 0) operations.push({ kind: 'delete', position: prefix, length: removed })
  if (inserted) operations.push({ kind: 'insert', position: prefix, text: inserted })
  return operations
}

function positionOverDelete(position: number, deletedAt: number, deletedLength: number): number {
	const end = deletedAt + deletedLength
	if (position <= deletedAt) return position
	if (position >= end) return position - deletedLength
	return deletedAt
}

export function transformOperation(incoming: Operation, applied: Operation): Operation {
	const transformed = { ...incoming }
	if (incoming.kind === 'insert' && applied.kind === 'insert') {
		if (applied.position <= transformed.position) transformed.position += codePoints(applied.text ?? '').length
	} else if (incoming.kind === 'insert' && applied.kind === 'delete') {
		transformed.position = positionOverDelete(transformed.position, applied.position, applied.length ?? 0)
	} else if (incoming.kind === 'delete' && applied.kind === 'insert') {
		const inserted = codePoints(applied.text ?? '').length
		const end = transformed.position + (transformed.length ?? 0)
		if (applied.position <= transformed.position) transformed.position += inserted
		else if (applied.position < end) transformed.length = (transformed.length ?? 0) + inserted
	} else if (incoming.kind === 'delete' && applied.kind === 'delete') {
		const start = positionOverDelete(transformed.position, applied.position, applied.length ?? 0)
		const end = positionOverDelete(transformed.position + (transformed.length ?? 0), applied.position, applied.length ?? 0)
		transformed.position = start
		transformed.length = Math.max(0, end - start)
	}
	return transformed
}
