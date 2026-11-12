import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AccessList } from '../components/AccessList'
import { ErrorBanner, LoadingState } from '../components/Feedback'
import { Icon } from '../components/Icon'
import { useDocumentStore } from '../stores/documentStore'
import * as workspaceApi from '../api/documents'
import type { DocumentSummary, Folder } from '../types/api'

function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Recently'
  return new Intl.RelativeTimeFormat('en', { numeric: 'auto' }).format(
    Math.round((date.getTime() - Date.now()) / 86_400_000),
    'day',
  )
}

function WorkspaceDialog({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const dialog = useRef<HTMLDialogElement>(null)
  useEffect(() => { dialog.current?.showModal() }, [])
  return <dialog ref={dialog} className="modal workspace-dialog" aria-labelledby="workspace-dialog-title" onClose={onClose}>
    <h2 id="workspace-dialog-title">{title}</h2>
    {children}
  </dialog>
}

export function DashboardPage() {
  const [query, setQuery] = useState('')
  const [creating, setCreating] = useState(false)
  const [newId, setNewId] = useState('')
  const [creatingFolder, setCreatingFolder] = useState(false)
  const [folderName, setFolderName] = useState('')
  const [folders, setFolders] = useState<Folder[]>([])
  const [activeFolder, setActiveFolder] = useState('')
  const [collapsedFolders, setCollapsedFolders] = useState<Set<string>>(() => new Set())
  const [workspaceError, setWorkspaceError] = useState('')
  const [sharing, setSharing] = useState<Folder | null>(null)
  const [folderAccess, setFolderAccess] = useState<workspaceApi.FolderAccess[]>([])
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<'read' | 'write' | 'revoke'>('read')
  const [moving, setMoving] = useState('')
  const [movingFolder, setMovingFolder] = useState(false)
  const [deletingFolder, setDeletingFolder] = useState<Folder | null>(null)
  const [renaming, setRenaming] = useState<{ id: string; kind: 'file' | 'folder' } | null>(null)
  const [fileName, setFileName] = useState('')
  const [savingName, setSavingName] = useState(false)
  const [destination, setDestination] = useState('')
  const [movePreview, setMovePreview] = useState<{ id: string; folder: string; changes: workspaceApi.AccessChange[] }>()
  const { documents, loading, error, load, create, remove, clearError } = useDocumentStore()
  const navigate = useNavigate()

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    void workspaceApi.listFolders().then(setFolders).catch((err: Error) => setWorkspaceError(err.message))
  }, [])
  const currentFolder = folders.find((folder) => folder.id === activeFolder)
  const childFolders = folders.filter((folder) => (folder.parent_id ?? '') === activeFolder)
  const dialogOpen = creating || creatingFolder || sharing || moving || renaming || deletingFolder
  const mayCreate = !currentFolder || currentFolder.role === 'owner'
  const documentsByFolder = useMemo(() => {
    const visibleFolders = new Set(folders.map((folder) => folder.id))
    const grouped = new Map<string, typeof documents>()
    for (const document of documents) {
      const folderId = document.folder_id && visibleFolders.has(document.folder_id) ? document.folder_id : ''
      const files = grouped.get(folderId)
      if (files) files.push(document)
      else grouped.set(folderId, [document])
    }
    return grouped
  }, [documents, folders])

  const runWorkspaceAction = async (action: () => Promise<void>) => {
    setWorkspaceError('')
    try {
      await action()
    } catch (err) {
      setWorkspaceError(err instanceof Error ? err.message : 'Workspace operation failed.')
    }
  }

  const addFolder = () => {
    const name = folderName.trim()
    if (!name) return
    void runWorkspaceAction(async () => {
      const folder = await workspaceApi.createFolder(name, activeFolder || undefined)
      setFolders((items) => [...items, folder])
      setActiveFolder(folder.id)
      setCreatingFolder(false)
      setFolderName('')
    })
  }

  const startRename = (item: DocumentSummary | Folder, kind: 'file' | 'folder') => {
    setWorkspaceError('')
    setFileName((item.name ?? item.id).split('/').pop() ?? '')
    setRenaming({ id: item.id, kind })
  }

  const folderTree = (parentId = '') => {
    const children = folders.filter((folder) => (folder.parent_id ?? '') === parentId)
    const files = documentsByFolder.get(parentId) ?? []
    if (!children.length && !files.length) return null
    return <ul>{children.map((folder) => {
      const hasChildren = documentsByFolder.has(folder.id) || folders.some((item) => item.parent_id === folder.id)
      const expanded = hasChildren && !collapsedFolders.has(folder.id)
      return <li key={folder.id}>
        <div className="workspace-tree-row">
          {hasChildren && <button type="button" className="workspace-tree-toggle" aria-expanded={expanded} aria-label={`${expanded ? 'Collapse' : 'Expand'} ${folder.name}`} onClick={() => setCollapsedFolders((previous) => {
            const next = new Set(previous)
            if (next.has(folder.id)) next.delete(folder.id)
            else next.add(folder.id)
            return next
          })}><Icon name="chevron" /></button>}
          <button type="button" className="ghost-button" title={folder.name} aria-pressed={activeFolder === folder.id} onClick={() => setActiveFolder(folder.id)}><Icon name="folder" /><span>{folder.name}</span></button>
          {folder.role === 'owner' && <button type="button" className="workspace-rename-button" title="Rename folder" aria-label={`Rename folder ${folder.name}`} onClick={() => startRename(folder, 'folder')}><Icon name="edit" /></button>}
        </div>
        {expanded && folderTree(folder.id)}
      </li>
    })}{files.map((document) => (
      <li key={document.id} className="workspace-tree-row">
        <button type="button" className="ghost-button workspace-tree-file" title={document.name ?? document.id} onClick={() => navigate(`/documents/${encodeURIComponent(document.id)}`)}>
          <Icon name="files" /><span>{document.name ?? document.id}</span>
        </button>
        {document.role === 'owner' && <button type="button" className="workspace-rename-button" title="Rename file" aria-label={`Rename ${document.name ?? document.id}`} onClick={() => startRename(document, 'file')}><Icon name="edit" /></button>}
      </li>
    ))}</ul>
  }

  const filtered = useMemo(
    () => {
      const term = query.trim().toLowerCase()
      if (!term) return documentsByFolder.get(activeFolder) ?? []
      return documents.filter((document) => (document.name ?? document.id).toLowerCase().includes(term))
    },
    [documents, documentsByFolder, activeFolder, query],
  )

  const submitCreate = async (event: FormEvent) => {
    event.preventDefault()
    const id = newId.trim()
    if (!id) return
    try {
      const documentId = await create(id, activeFolder || undefined)
      setNewId('')
      setCreating(false)
      navigate(`/documents/${encodeURIComponent(documentId)}`)
    } catch {
      // Store owns the error message.
    }
  }

  const openFolderAccess = (folder: Folder) => {
    setFolderAccess([])
    setSharing(folder)
    void runWorkspaceAction(async () => setFolderAccess(await workspaceApi.listFolderAccess(folder.id)))
  }

  const canMoveInto = (folder: Folder) => {
    if (folder.role !== 'owner') return false
    if (!movingFolder) return true
    for (let ancestor: Folder | undefined = folder; ancestor; ancestor = folders.find((item) => item.id === ancestor?.parent_id)) {
      if (ancestor.id === moving) return false
    }
    return true
  }

  return (
    <AppShell>
      <div className="dashboard-page">
        <header className="page-header workspace-header">
          <h1>Workspace</h1>
        </header>

        <div className="dashboard-toolbar">
          <div className="search-field"><Icon name="search" /><input aria-label="Search documents" placeholder="Search documents…" value={query} onChange={(event) => setQuery(event.target.value)} /></div>
        </div>

        {!dialogOpen && <ErrorBanner message={error || workspaceError} onDismiss={() => { clearError(); setWorkspaceError('') }} />}
        <div className="workspace-layout">
        <nav className="workspace-folders" aria-label="File explorer">
          <button type="button" className="ghost-button workspace-root-button" aria-label="Workspace root" title="Workspace root" aria-pressed={!activeFolder} onClick={() => setActiveFolder('')}><Icon name="folder" /></button>
          {folderTree()}
        </nav>
        <div className="workspace-files">
        <div className="workspace-directory-header">
          <h2>{query.trim() ? 'Search results' : currentFolder?.name ?? 'Workspace root'}</h2>
          <div className="workspace-directory-actions">
            {currentFolder?.role === 'owner' && <>
              <button type="button" className="ghost-button" onClick={() => openFolderAccess(currentFolder)}><Icon name="share" /> Share</button>
              <button type="button" className="ghost-button" onClick={() => {
                setWorkspaceError('')
                setMovePreview(undefined)
                setDestination(currentFolder.parent_id ?? '')
                setMovingFolder(true)
                setMoving(currentFolder.id)
              }}>Move folder</button>
              {childFolders.length === 0 && !documentsByFolder.has(currentFolder.id) && <button type="button" className="ghost-button" onClick={() => { setWorkspaceError(''); setDeletingFolder(currentFolder) }}>Delete folder</button>}
            </>}
            {mayCreate && <button type="button" className="secondary-button" onClick={() => { setWorkspaceError(''); setCreatingFolder(true) }}><Icon name="folder" /> New folder</button>}
            {mayCreate && <button type="button" className="primary-button" onClick={() => { clearError(); setCreating(true) }}><Icon name="plus" /> New document</button>}
          </div>
        </div>
        {creatingFolder && <WorkspaceDialog title="New folder" onClose={() => setCreatingFolder(false)}>
          <ErrorBanner message={workspaceError} onDismiss={() => setWorkspaceError('')} />
          <form className="workspace-form" onSubmit={(event) => { event.preventDefault(); addFolder() }}>
            <label>Folder name<input autoFocus value={folderName} onChange={(event) => setFolderName(event.target.value)} required /></label>
            <div className="modal-actions"><button type="button" className="ghost-button" onClick={() => setCreatingFolder(false)}>Cancel</button><button className="primary-button">Create folder</button></div>
          </form>
        </WorkspaceDialog>}
        {sharing && (
          <WorkspaceDialog title={`Share ${sharing.name}`} onClose={() => setSharing(null)}>
          <p>Access applies to existing and future files.</p>
          <ErrorBanner message={workspaceError} onDismiss={() => setWorkspaceError('')} />
          <form className="workspace-form" onSubmit={(event) => {
            event.preventDefault()
            void runWorkspaceAction(async () => {
              await workspaceApi.shareFolder(sharing.id, username.trim(), role)
              setFolderAccess(await workspaceApi.listFolderAccess(sharing.id))
              setUsername('')
            })
          }}>
            <label>Username<input autoFocus value={username} onChange={(event) => setUsername(event.target.value)} required /></label>
            <label>Access<select value={role} onChange={(event) => setRole(event.target.value as typeof role)}>
              <option value="read">Can view</option><option value="write">Can edit</option><option value="revoke">Remove folder access</option>
            </select></label>
            <div className="modal-actions"><button type="button" className="ghost-button" onClick={() => setSharing(null)}>Cancel</button><button className="primary-button">Save access</button></div>
          </form>
          <AccessList grants={folderAccess} directFolderId={sharing.id} onChange={(id, next) => void runWorkspaceAction(async () => {
            await workspaceApi.changeFolderAccess(sharing.id, id, next)
            setFolderAccess(await workspaceApi.listFolderAccess(sharing.id))
          })} />
          </WorkspaceDialog>
        )}
        {deletingFolder && <WorkspaceDialog title={`Delete ${deletingFolder.name}?`} onClose={() => setDeletingFolder(null)}>
          <ErrorBanner message={workspaceError} onDismiss={() => setWorkspaceError('')} />
          <p>Only empty folders can be deleted.</p>
          <div className="modal-actions">
            <button type="button" className="ghost-button" onClick={() => setDeletingFolder(null)}>Cancel</button>
            <button type="button" className="primary-button" onClick={() => void runWorkspaceAction(async () => {
              await workspaceApi.deleteEmptyFolder(deletingFolder.id)
              setFolders((items) => items.filter((folder) => folder.id !== deletingFolder.id))
              setActiveFolder(deletingFolder.parent_id ?? '')
              setDeletingFolder(null)
            })}>Delete folder</button>
          </div>
        </WorkspaceDialog>}
        {moving && (
          <WorkspaceDialog title={movingFolder ? 'Move folder' : 'Move document'} onClose={() => { setMoving(''); setMovePreview(undefined) }}>
          <ErrorBanner message={workspaceError} onDismiss={() => setWorkspaceError('')} />
          <form className="workspace-form" onSubmit={(event) => {
            event.preventDefault()
            void runWorkspaceAction(async () => {
              if (movePreview?.id !== moving || movePreview.folder !== destination) {
                const changes = await (movingFolder ? workspaceApi.previewFolderMove : workspaceApi.previewDocumentMove)(moving, destination)
                setMovePreview({ id: moving, folder: destination, changes })
                return
              }
              if (movingFolder) {
                await workspaceApi.moveFolder(moving, destination)
                setFolders(await workspaceApi.listFolders())
              } else {
                await workspaceApi.moveDocument(moving, destination)
              }
              setMoving('')
              setMovePreview(undefined)
              await load()
            })
          }}>
            <label>Destination<select value={destination} onChange={(event) => setDestination(event.target.value)}>
              <option value="">Workspace root</option>
              {folders.filter(canMoveInto).map((folder) => <option key={folder.id} value={folder.id}>{folder.name}</option>)}
            </select></label>
            {movePreview?.id === moving && movePreview.folder === destination && (
              movePreview.changes.length === 0 ? <p>No access changes.</p> : (
                <ul>{movePreview.changes.map((change) => (
                  <li key={change.user_id}>{change.username ?? change.user_id}: {change.before || 'no access'} → {change.after || 'no access'}</li>
                ))}</ul>
              )
            )}
            <div className="modal-actions">
            <button type="button" className="ghost-button" onClick={() => { setMoving(''); setMovePreview(undefined) }}>Cancel</button>
            <button className="primary-button">{movePreview?.id === moving && movePreview.folder === destination ? 'Confirm move' : 'Review access changes'}</button>
            </div>
          </form>
          </WorkspaceDialog>
        )}
        {loading && documents.length === 0 ? <LoadingState label="Loading your documents…" /> : filtered.length === 0 && (query.trim() || childFolders.length === 0) ? (
          <p className="workspace-empty">{query.trim() ? 'No matching documents' : 'This folder is empty'}</p>
        ) : (
          <div className="document-grid">
            {!query.trim() && childFolders.map((folder) => (
              <button key={folder.id} type="button" className="workspace-folder-row" onClick={() => setActiveFolder(folder.id)}>
                <Icon name="folder" /><span>{folder.name}</span><Icon name="chevron" />
              </button>
            ))}
            {filtered.map((document) => (
              <article className="document-card" key={document.id} onClick={() => navigate(`/documents/${encodeURIComponent(document.id)}`)}>
                <div className="document-icon"><Icon name="files" /></div>
                <div className="document-card-body"><h3>{document.name ?? document.id}</h3><p>Version {document.version} · {formatTime(document.updated_at)}</p></div>
                <span className={`role-badge role-${document.role}`}>{document.role}</span>
                {document.role === 'owner' && folders.length > 0 && <button className="ghost-button" onClick={(event) => {
                  event.stopPropagation()
                  setWorkspaceError('')
                  setMovePreview(undefined)
                  setMovingFolder(false)
                  setDestination(document.folder_id ?? '')
                  setMoving(document.id)
                }}>Move</button>}
                {document.role === 'owner' && <button className="card-action danger" aria-label={`Delete ${document.id}`} onClick={(event) => { event.stopPropagation(); if (window.confirm(`Delete ${document.id}? This cannot be undone.`)) void remove(document.id) }}><Icon name="trash" /></button>}
                <Icon name="chevron" className="card-chevron" />
              </article>
            ))}
          </div>
        )}

        </div>
        </div>
        {renaming && <WorkspaceDialog title={`Rename ${renaming.kind}`} onClose={() => setRenaming(null)}>
          <ErrorBanner message={workspaceError} onDismiss={() => setWorkspaceError('')} />
          <form className="workspace-form" onSubmit={(event) => {
            event.preventDefault()
            const name = fileName.trim()
            if (!name || savingName) return
            setSavingName(true)
            void runWorkspaceAction(async () => {
              if (renaming.kind === 'folder') {
                await workspaceApi.renameFolder(renaming.id, name)
                setFolders((items) => items.map((folder) => folder.id === renaming.id ? { ...folder, name } : folder))
              } else {
                await workspaceApi.renameDocument(renaming.id, name)
                await load()
              }
              setRenaming(null)
            }).finally(() => setSavingName(false))
          }}>
            <label>{renaming.kind === 'folder' ? 'Folder name' : 'File name'}<input autoFocus value={fileName} onChange={(event) => setFileName(event.target.value)} maxLength={200} required /></label>
            <div className="modal-actions"><button type="button" className="ghost-button" onClick={() => setRenaming(null)}>Cancel</button><button className="primary-button" disabled={savingName || !fileName.trim()}>Save name</button></div>
          </form>
        </WorkspaceDialog>}
        {creating && <WorkspaceDialog title="New document" onClose={() => setCreating(false)}>
          <ErrorBanner message={error} onDismiss={clearError} />
          <form className="workspace-form" onSubmit={(event) => void submitCreate(event)}>
            <label>File name<input autoFocus value={newId} onChange={(event) => setNewId(event.target.value)} placeholder={currentFolder ? 'main.go' : 'project/main.go'} required /></label>
            <div className="modal-actions"><button type="button" className="ghost-button" onClick={() => setCreating(false)}>Cancel</button><button className="primary-button" disabled={loading}>Create and open</button></div>
          </form>
        </WorkspaceDialog>}
      </div>
    </AppShell>
  )
}
