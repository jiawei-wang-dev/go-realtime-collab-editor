import { useEffect, useMemo, useRef, useState } from 'react'
import { listDocuments, listFolders } from '../api/documents'
import { Icon } from './Icon'
import type { DocumentSummary, Folder } from '../types/api'

export function QuickOpen({ userId, activeId, onOpen }: { userId: string; activeId: string; onOpen: (id: string) => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [files, setFiles] = useState<DocumentSummary[]>([])
  const [folders, setFolders] = useState<Folder[]>([])
  const [query, setQuery] = useState('')
  const [index, setIndex] = useState(0)
  const [status, setStatus] = useState('')
  const [recent, setRecent] = useState<string[]>([])
  const request = useRef(0)

  useEffect(() => {
    if (!userId || !activeId) return
    const key = `workspace-recent:${userId}`
    let ids: string[] = []
    try {
      const saved: unknown = JSON.parse(localStorage.getItem(key) ?? '[]')
      if (Array.isArray(saved)) ids = saved.filter((id): id is string => typeof id === 'string')
    } catch { /* Use an empty list when storage is unavailable. */ }
    const next = [activeId, ...ids.filter((id) => id !== activeId)].slice(0, 20)
    setRecent(next)
    try { localStorage.setItem(key, JSON.stringify(next)) } catch { /* Recent files are optional. */ }
  }, [userId, activeId])

  const open = () => {
    const element = dialog.current
    if (!element || element.open || document.querySelector('dialog[open]')) return
    element.showModal()
    setQuery('')
    setIndex(0)
    setFiles([])
    setStatus('Loading files…')
    const sequence = ++request.current
    void Promise.all([listDocuments(), listFolders()]).then(([documents, directories]) => {
      if (request.current !== sequence) return
      setFiles(documents)
      setFolders(directories)
      setStatus('')
    }).catch(() => {
      if (request.current === sequence) setStatus('Unable to load files.')
    })
  }

  useEffect(() => {
    const shortcut = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'p') {
        event.preventDefault()
        open()
      }
    }
    window.addEventListener('keydown', shortcut, true)
    return () => window.removeEventListener('keydown', shortcut, true)
  }, [])

  const results = useMemo(() => {
    const directoryById = new Map(folders.map((folder) => [folder.id, folder]))
    const ranks = new Map(recent.map((id, rank) => [id, rank]))
    const term = query.trim().toLowerCase()
    return files.map((file) => {
      const name = file.name ?? file.id
      const parts = [name]
      const visited = new Set<string>()
      let parent = file.folder_id
      while (parent && !visited.has(parent)) {
        visited.add(parent)
        const folder = directoryById.get(parent)
        if (!folder) break
        parts.unshift(folder.name)
        parent = folder.parent_id
      }
      return { id: file.id, path: parts.join('/') }
    }).filter((file) => file.path.toLowerCase().includes(term))
      .sort((a, b) => (ranks.get(a.id) ?? 20) - (ranks.get(b.id) ?? 20) || a.path.localeCompare(b.path))
      .slice(0, 100)
  }, [files, folders, query, recent])

  const choose = (id: string) => {
    dialog.current?.close()
    onOpen(id)
  }

  return <>
    <button type="button" className="ghost-button" title="Quick Open (Ctrl/⌘+P)" aria-label="Quick Open" onClick={open}><Icon name="search" /></button>
    <dialog ref={dialog} className="modal workspace-dialog quick-open" aria-label="Quick Open" onClose={() => { request.current++ }}>
      <input autoFocus aria-label="Find a file by name or path" placeholder="Find a file by name or path…" value={query} onChange={(event) => { setQuery(event.target.value); setIndex(0) }} onKeyDown={(event) => {
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          event.preventDefault()
          if (!results.length) return
          const next = (index + (event.key === 'ArrowDown' ? 1 : -1) + results.length) % results.length
          setIndex(next)
          dialog.current?.querySelectorAll('.quick-open-result')[next]?.scrollIntoView({ block: 'nearest' })
        } else if (event.key === 'Enter' && results[index]) {
          event.preventDefault()
          choose(results[index].id)
        }
      }} />
      {status || !results.length ? <p role="status">{status || 'No matching files'}</p> : <ul className="quick-open-results">
        {results.map((file, i) => <li key={file.id}><button type="button" className="quick-open-result" aria-current={i === index} onClick={() => choose(file.id)}>{file.path}</button></li>)}
      </ul>}
    </dialog>
  </>
}
