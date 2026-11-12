import { useEffect, useState, type FormEvent } from 'react'
import { changeDocumentAccess, listDocumentAccess, lookupUser, type FolderAccess } from '../api/documents'
import { AccessList } from './AccessList'
import { ErrorBanner, LoadingState } from './Feedback'

export function ShareDialog({ documentId, name, onClose }: { documentId: string; name?: string; onClose: () => void }) {
  const [grants, setGrants] = useState<FolderAccess[]>([])
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<'write' | 'read'>('write')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    void listDocumentAccess(documentId).then(setGrants).catch((reason: unknown) => {
      setError(reason instanceof Error ? reason.message : 'Unable to load access.')
    }).finally(() => setLoading(false))
  }, [documentId])

  const change = async (action: () => Promise<void>) => {
    setLoading(true)
    setError('')
    try {
      await action()
      setGrants(await listDocumentAccess(documentId))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to update access.')
    } finally {
      setLoading(false)
    }
  }

  const invite = (event: FormEvent) => {
    event.preventDefault()
    void change(async () => {
      const user = await lookupUser(username.trim())
      await changeDocumentAccess(documentId, user.id, role)
      setUsername('')
    })
  }

  return <div className="modal-backdrop" onMouseDown={onClose}>
    <section className="modal share-modal" onMouseDown={(event) => event.stopPropagation()} role="dialog" aria-modal="true" aria-labelledby="share-title">
      <h2 id="share-title">Share {name ?? documentId}</h2>
      <ErrorBanner message={error} onDismiss={() => setError('')} />
      <form className="share-form" onSubmit={invite}>
        <input aria-label="Username" value={username} onChange={(event) => setUsername(event.target.value)} placeholder="username" required />
        <select aria-label="Permission" value={role} onChange={(event) => setRole(event.target.value as 'write' | 'read')}><option value="write">Can edit</option><option value="read">Can view</option></select>
        <button className="primary-button" disabled={loading}>Share</button>
      </form>
      {loading && grants.length === 0 ? <LoadingState label="Loading access…" /> : <AccessList grants={grants} busy={loading} onChange={(id, next) => void change(() => changeDocumentAccess(documentId, id, next))} />}
      <div className="modal-actions"><button className="secondary-button" onClick={onClose}>Done</button></div>
    </section>
  </div>
}
