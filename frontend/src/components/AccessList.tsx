import type { FolderAccess } from '../api/documents'

export function AccessList({ grants, directFolderId = '', busy, onChange }: {
  grants: FolderAccess[]
  directFolderId?: string
  busy?: boolean
  onChange: (userId: string, role: 'read' | 'write' | 'revoke') => void
}) {
  return <ul className="folder-access-list">
    {grants.map((grant) => {
      const direct = grant.folder_id === directFolderId
      const inherited = grants.filter((item) => item.user_id === grant.user_id && item.folder_id !== directFolderId)
      const remaining = inherited.some((item) => item.role === 'write') ? 'Can edit' : inherited.length ? 'Can view' : ''
      const role = grant.role === 'owner' ? 'Owner' : grant.role === 'write' ? 'Can edit' : 'Can view'
      return <li key={`${grant.user_id}:${grant.folder_id}`}>
        <div><strong>{grant.username}</strong><br /><small>{direct ? 'Direct access' : `From ${grant.folder_name}`}</small></div>
        {direct && grant.role !== 'owner'
          ? <select aria-label={`Access for ${grant.username}`} disabled={busy} value={grant.role} onChange={(event) => onChange(grant.user_id, event.target.value as 'read' | 'write' | 'revoke')}>
            <option value="read">Can view</option><option value="write">Can edit</option><option value="revoke">{remaining ? `Remove direct access (keeps ${remaining.toLowerCase()})` : 'Remove access'}</option>
          </select>
          : <span>{role}</span>}
      </li>
    })}
  </ul>
}
