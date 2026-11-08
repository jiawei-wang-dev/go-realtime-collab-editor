export function createRequestId(prefix = 'web'): string {
  return `${prefix}-${crypto.randomUUID()}`
}
