import { Icon } from './Icon'

export function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <div className="brand" aria-label="Collab">
      <span className="brand-mark"><Icon name="code" /></span>
      {!compact && <span className="brand-name">collab<span className="brand-dot">.</span></span>}
    </div>
  )
}
