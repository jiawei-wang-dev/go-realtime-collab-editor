export function LoadingState({ label = 'Loading…' }: { label?: string }) {
  return <div className="state-panel"><span className="spinner" /> <span>{label}</span></div>
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: React.ReactNode }) {
  return <div className="empty-state"><div className="empty-orbit"><span /></div><h3>{title}</h3><p>{description}</p>{action}</div>
}

export function ErrorBanner({ message, onDismiss }: { message: string; onDismiss?: () => void }) {
  if (!message) return null
  return <div className="error-banner" role="alert"><span>{message}</span>{onDismiss && <button onClick={onDismiss} aria-label="Dismiss error">×</button>}</div>
}
