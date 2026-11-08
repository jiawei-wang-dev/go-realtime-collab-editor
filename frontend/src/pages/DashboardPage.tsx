import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { EmptyState, ErrorBanner, LoadingState } from '../components/Feedback'
import { Icon } from '../components/Icon'
import { useAuthStore } from '../stores/authStore'
import { useDocumentStore } from '../stores/documentStore'

function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Recently'
  return new Intl.RelativeTimeFormat('en', { numeric: 'auto' }).format(
    Math.round((date.getTime() - Date.now()) / 86_400_000),
    'day',
  )
}

export function DashboardPage() {
  const [query, setQuery] = useState('')
  const [creating, setCreating] = useState(false)
  const [newId, setNewId] = useState('')
  const user = useAuthStore((state) => state.user)
  const { documents, loading, error, load, create, remove, clearError } = useDocumentStore()
  const navigate = useNavigate()

  useEffect(() => { void load() }, [load])

  const filtered = useMemo(
    () => documents.filter((document) => document.id.toLowerCase().includes(query.trim().toLowerCase())),
    [documents, query],
  )

  const submitCreate = async (event: FormEvent) => {
    event.preventDefault()
    const id = newId.trim()
    if (!id) return
    try {
      await create(id)
      setNewId('')
      setCreating(false)
      navigate(`/documents/${encodeURIComponent(id)}`)
    } catch {
      // Store owns the error message.
    }
  }

  return (
    <AppShell>
      <div className="dashboard-page">
        <header className="page-header">
          <div><span className="eyebrow">WORKSPACE</span><h1>Good to see you, {user?.username}.</h1><p>Pick up where you left off or start something new.</p></div>
          <button className="primary-button" onClick={() => setCreating(true)}><Icon name="plus" /> New document</button>
        </header>

        <div className="dashboard-toolbar">
          <div className="search-field"><Icon name="search" /><input aria-label="Search documents" placeholder="Search documents…" value={query} onChange={(event) => setQuery(event.target.value)} /></div>
          <span className="document-count">{documents.length} {documents.length === 1 ? 'document' : 'documents'}</span>
        </div>

        <ErrorBanner message={error} onDismiss={clearError} />
        {loading && documents.length === 0 ? <LoadingState label="Loading your documents…" /> : filtered.length === 0 ? (
          <EmptyState title={query ? 'No matching documents' : 'Your workspace is ready'} description={query ? 'Try another search term.' : 'Create your first document and invite someone to edit with you.'} action={!query && <button className="secondary-button" onClick={() => setCreating(true)}><Icon name="plus" /> Create document</button>} />
        ) : (
          <div className="document-grid">
            {filtered.map((document) => (
              <article className="document-card" key={document.id} onClick={() => navigate(`/documents/${encodeURIComponent(document.id)}`)}>
                <div className="document-icon"><Icon name="code" /></div>
                <div className="document-card-body"><h3>{document.id}</h3><p>Version {document.version} · {formatTime(document.updated_at)}</p></div>
                <span className={`role-badge role-${document.role}`}>{document.role}</span>
                {document.role === 'owner' && <button className="card-action danger" aria-label={`Delete ${document.id}`} onClick={(event) => { event.stopPropagation(); if (window.confirm(`Delete ${document.id}? This cannot be undone.`)) void remove(document.id) }}><Icon name="trash" /></button>}
                <Icon name="chevron" className="card-chevron" />
              </article>
            ))}
          </div>
        )}

        {creating && <div className="modal-backdrop" onMouseDown={() => setCreating(false)}><section className="modal" onMouseDown={(event) => event.stopPropagation()} role="dialog" aria-modal="true" aria-labelledby="new-document-title"><span className="eyebrow">NEW DOCUMENT</span><h2 id="new-document-title">Name your document</h2><p>Use a stable ID such as <code>service/main.go</code> or <code>notes/architecture.md</code>.</p><form onSubmit={(event) => void submitCreate(event)}><input autoFocus value={newId} onChange={(event) => setNewId(event.target.value)} placeholder="project/main.go" required /><div className="modal-actions"><button type="button" className="ghost-button" onClick={() => setCreating(false)}>Cancel</button><button className="primary-button" disabled={loading}>Create and open</button></div></form></section></div>}
      </div>
    </AppShell>
  )
}
