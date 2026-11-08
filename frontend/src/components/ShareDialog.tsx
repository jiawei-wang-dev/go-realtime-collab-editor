import { useEffect, useState, type FormEvent } from 'react'
import { listPermissions, lookupUser, updatePermission } from '../api/documents'
import type { DocumentPermission, PermissionRole } from '../types/api'
import { Avatar } from './Avatar'
import { ErrorBanner, LoadingState } from './Feedback'

export function ShareDialog({ documentId, onClose }: { documentId: string; onClose: () => void }) {
  const [permissions, setPermissions] = useState<DocumentPermission[]>([])
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<Exclude<PermissionRole, 'owner'>>('write')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    void listPermissions(documentId).then(setPermissions).catch((reason: unknown) => setError(reason instanceof Error ? reason.message : 'Unable to load permissions.')).finally(() => setLoading(false))
  }, [documentId])

  const invite = async (event: FormEvent) => {
    event.preventDefault()
    setLoading(true)
    setError('')
    try {
      const user = await lookupUser(username.trim())
      const permission = await updatePermission(documentId, user.id, role)
      setPermissions((current) => [...current.filter((item) => item.user_id !== user.id), { ...permission, username: user.username }])
      setUsername('')
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to update access.')
    } finally {
      setLoading(false)
    }
  }

  return <div className="modal-backdrop" onMouseDown={onClose}><section className="modal share-modal" onMouseDown={(event) => event.stopPropagation()} role="dialog" aria-modal="true" aria-labelledby="share-title"><span className="eyebrow">ACCESS</span><h2 id="share-title">Share {documentId}</h2><p>Invite an existing user by exact username.</p><ErrorBanner message={error} onDismiss={() => setError('')} /><form className="share-form" onSubmit={(event) => void invite(event)}><input value={username} onChange={(event) => setUsername(event.target.value)} placeholder="username" required /><select value={role} onChange={(event) => setRole(event.target.value as 'write' | 'read')}><option value="write">Can edit</option><option value="read">Can view</option></select><button className="primary-button" disabled={loading}>Share</button></form><div className="permission-list">{loading && permissions.length === 0 ? <LoadingState label="Loading access…" /> : permissions.map((permission) => <div className="permission-row" key={permission.user_id}><Avatar name={permission.username ?? permission.user_id} size="sm" /><div><strong>{permission.username ?? permission.user_id}</strong><span>{permission.user_id}</span></div><span className={`role-badge role-${permission.role}`}>{permission.role}</span></div>)}</div><div className="modal-actions"><button className="secondary-button" onClick={onClose}>Done</button></div></section></div>
}
