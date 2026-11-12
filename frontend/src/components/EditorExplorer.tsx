import { useEffect, useState } from 'react'
import { listDocuments, listFolders } from '../api/documents'
import type { DocumentSummary, Folder } from '../types/api'
import { Icon } from './Icon'

export function EditorExplorer({ activeId, onOpen }: { activeId: string; onOpen: (id: string) => void }) {
  const [documents, setDocuments] = useState<DocumentSummary[]>([])
  const [folders, setFolders] = useState<Folder[]>([])
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    void Promise.all([listDocuments(), listFolders()]).then(([files, directories]) => {
      if (cancelled) return
      setDocuments(files)
      setFolders(directories)
      setError('')
    }).catch((reason: unknown) => {
      if (!cancelled) setError(reason instanceof Error ? reason.message : 'Unable to load files.')
    })
    return () => { cancelled = true }
  }, [activeId])

  const visible = new Set(folders.map((folder) => folder.id))
  const children = (parent: string) => (
    <ul>
      {folders.filter((folder) => (folder.parent_id ?? '') === parent).map((folder) => (
        <li key={folder.id}>
          <button type="button" className="file-entry" aria-expanded={!collapsed.has(folder.id)} onClick={() => setCollapsed((previous) => {
            const next = new Set(previous)
            if (next.has(folder.id)) next.delete(folder.id)
            else next.add(folder.id)
            return next
          })}><Icon name="chevron" /><Icon name="folder" /><span>{folder.name}</span></button>
          {!collapsed.has(folder.id) && children(folder.id)}
        </li>
      ))}
      {documents.filter((file) => (file.folder_id && visible.has(file.folder_id) ? file.folder_id : '') === parent).map((file) => (
        <li key={file.id}>
          <button type="button" className={`file-entry${file.id === activeId ? ' active' : ''}`} aria-current={file.id === activeId ? 'page' : undefined} onClick={() => onOpen(file.id)}>
            <Icon name="files" /><span>{file.name ?? file.id}</span>
          </button>
        </li>
      ))}
    </ul>
  )

  return <nav className="editor-explorer" aria-label="Editor files">
    {error ? <p className="panel-empty" role="alert">{error}</p> : children('')}
  </nav>
}
