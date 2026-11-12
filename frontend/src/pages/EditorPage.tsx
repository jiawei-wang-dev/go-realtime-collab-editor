import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { getDocument } from '../api/documents'
import { runGoDocument } from '../api/execution'
import { Avatar } from '../components/Avatar'
import { Brand } from '../components/Brand'
import { ErrorBanner, LoadingState } from '../components/Feedback'
import { EditorExplorer } from '../components/EditorExplorer'
import { DocumentHistory } from '../components/DocumentHistory'
import { QuickOpen } from '../components/QuickOpen'
import { Icon } from '../components/Icon'
import { ShareDialog } from '../components/ShareDialog'
import { RunConsole } from '../components/RunConsole'
import { MonacoWorkspace } from '../editor/MonacoWorkspace'
import { RemoteCursorDecorations, utf16OffsetToCodePoint } from '../editor/remoteCursors'
import { useAuthStore } from '../stores/authStore'
import { useCollaborationStore } from '../stores/collaborationStore'
import { RealtimeSession } from '../services/realtimeSession'
import type { CursorState } from '../types/protocol'
import type { RunResult } from '../types/api'
import type { editor } from 'monaco-editor'

function languageFor(documentId: string): string {
  const extension = documentId.split('.').pop()?.toLowerCase()
  return ({ ts: 'typescript', tsx: 'typescript', js: 'javascript', jsx: 'javascript', go: 'go', py: 'python', rs: 'rust', json: 'json', md: 'markdown', css: 'css', html: 'html', yaml: 'yaml', yml: 'yaml' } as Record<string, string>)[extension ?? ''] ?? 'plaintext'
}

export function EditorPage() {
  const { documentId = '' } = useParams()
  const navigate = useNavigate()
  const decodedId = useMemo(() => decodeURIComponent(documentId), [documentId])
  const [shareOpen, setShareOpen] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [file, setFile] = useState<{ id: string; name: string }>()
  const [running, setRunning] = useState(false)
  const [runResult, setRunResult] = useState<RunResult | null>(null)
  const [runError, setRunError] = useState('')
  const [outputOpen, setOutputOpen] = useState(false)
  const user = useAuthStore((state) => state.user)
  const collaboration = useCollaborationStore()
  const sessionRef = useRef<RealtimeSession | null>(null)
  const cursorDecorationsRef = useRef<RemoteCursorDecorations | null>(null)
  const remoteCursorsRef = useRef(new Map<string, CursorState>())
  const selectionListenerRef = useRef<{ dispose: () => void } | null>(null)
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null)
  const switching = useRef(false)

  useEffect(() => {
    let cancelled = false
    const remoteCursors = remoteCursorsRef.current
    collaboration.beginLoad(decodedId)
    void getDocument(decodedId).then((document) => {
      if (!cancelled) {
        setFile({ id: decodedId, name: document.name ?? decodedId })
        collaboration.initialize(document.id, document.content, document.version)
        if (!user) return
        const session = new RealtimeSession(decodedId, user.id, {
          cursorChanged: (cursor) => {
            if (!cursor.user_id) return
            remoteCursors.set(cursor.user_id, cursor)
            cursorDecorationsRef.current?.update(cursor)
          },
          cursorRemoved: (userID) => {
            remoteCursors.delete(userID)
            cursorDecorationsRef.current?.remove(userID)
          },
        })
        sessionRef.current = session
        void session.start().catch((reason: unknown) => {
          if (!cancelled) collaboration.setError(reason instanceof Error ? reason.message : 'Unable to restore pending edits.')
        })
      }
    }).catch((reason: unknown) => {
      if (!cancelled) collaboration.setError(reason instanceof Error ? reason.message : 'Unable to open the document.')
    })
    return () => {
      cancelled = true
      sessionRef.current?.stop()
      sessionRef.current = null
      selectionListenerRef.current?.dispose()
      selectionListenerRef.current = null
      cursorDecorationsRef.current?.dispose()
      cursorDecorationsRef.current = null
      editorRef.current = null
      remoteCursors.clear()
      collaboration.reset()
    }
    // Store actions are stable; document identity controls this lifecycle.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [decodedId, user])

  const connected = collaboration.connection === 'CONNECTED'
  const readOnly = collaboration.role === 'read' || collaboration.role === ''
  const fileName = file?.id === decodedId ? file.name : decodedId
  const language = languageFor(fileName)
  const canRun = language === 'go' && !readOnly && !collaboration.loading

  const openFile = async (id: string) => {
    if (id === decodedId || switching.current) return
    switching.current = true
    try {
      await sessionRef.current?.flushEdits()
      setShareOpen(false)
      setHistoryOpen(false)
      setOutputOpen(false)
      navigate(`/documents/${encodeURIComponent(id)}`)
    } catch (reason) {
      collaboration.setError(reason instanceof Error ? reason.message : 'Unable to save edits before switching files.')
    } finally {
      switching.current = false
    }
  }

  const runDocument = async () => {
    if (!canRun || running) return
    setRunning(true)
    setRunError('')
    setRunResult(null)
    setOutputOpen(true)
    try {
      const source = editorRef.current?.getValue() ?? collaboration.content
      setRunResult(await runGoDocument(decodedId, source))
    } catch (reason) {
      setRunError(reason instanceof Error ? reason.message : 'Unable to run this document.')
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="editor-page">
      <header className="editor-topbar">
        <Link to="/" className="editor-brand"><Brand compact /><span className="breadcrumb">Workspace</span><span>/</span><strong>{fileName}</strong></Link>
        <div className="editor-actions"><span className={`connection-pill connection-${collaboration.connection.toLowerCase()}`}><span /> {collaboration.connection.replace('_', ' ').toLowerCase()}</span><span className={`sync-pill ${collaboration.synchronized ? 'is-synced' : ''}`}>{collaboration.pendingCount > 0 ? `${collaboration.pendingCount} pending` : collaboration.synchronized ? 'Synced' : 'Syncing'}</span>{language === 'go' && <button className="run-button" onClick={() => { void runDocument() }} disabled={!canRun || running}>{running ? 'Running…' : '▶ Run'}</button>}<button className="secondary-button" onClick={() => setShareOpen(true)} disabled={collaboration.role !== 'owner'}><Icon name="share" /> Share</button><Link to="/settings" className="avatar-link" aria-label="Account settings"><Avatar name={user?.username ?? 'User'} /></Link></div>
      </header>
      <div className="editor-workspace">
        <aside className="file-panel"><div className="panel-title"><span>EXPLORER</span><QuickOpen userId={user?.id ?? ''} activeId={decodedId} onOpen={(id) => void openFile(id)} /><Link to="/" title="All documents"><Icon name="files" /></Link></div><EditorExplorer activeId={decodedId} onOpen={(id) => void openFile(id)} /><div className="file-panel-meta"><button type="button" className="ghost-button" onClick={() => setHistoryOpen(true)} disabled={!collaboration.role}>History</button><code>v{collaboration.version}</code></div></aside>
        <main className="monaco-panel"><div className="monaco-editor-region">{collaboration.loading ? <LoadingState label="Opening document…" /> : collaboration.error ? <ErrorBanner message={collaboration.error} /> : <MonacoWorkspace value={collaboration.content} language={language} readOnly={readOnly} onChange={(value) => { void sessionRef.current?.updateContent(value) }} onMount={(editor, monaco) => {
          editorRef.current = editor
          selectionListenerRef.current?.dispose()
          cursorDecorationsRef.current?.dispose()
          const decorations = new RemoteCursorDecorations(editor, monaco)
          cursorDecorationsRef.current = decorations
          for (const cursor of remoteCursorsRef.current.values()) decorations.update(cursor)
          selectionListenerRef.current = editor.onDidChangeCursorSelection((event) => {
            const model = editor.getModel()
            if (!model) return
            const content = model.getValue()
            const anchorOffset = model.getOffsetAt({ lineNumber: event.selection.selectionStartLineNumber, column: event.selection.selectionStartColumn })
            const headOffset = model.getOffsetAt({ lineNumber: event.selection.positionLineNumber, column: event.selection.positionColumn })
            sessionRef.current?.updateCursor(utf16OffsetToCodePoint(content, anchorOffset), utf16OffsetToCodePoint(content, headOffset))
          })
        }} />}{!collaboration.loading && !connected && !collaboration.error && <div className="editor-notice">Connecting to the collaboration gateway…</div>}</div>{outputOpen && <RunConsole result={runResult} error={runError} running={running} onClear={() => { setRunResult(null); setRunError(''); setOutputOpen(false) }} />}</main>
        <aside className="collaborator-panel"><div className="panel-title"><span>COLLABORATORS</span><Icon name="users" /></div><div className="participant-list">{collaboration.participants.length === 0 ? <p className="panel-empty">No active collaborators yet.</p> : collaboration.participants.map((participant) => <div className="participant" key={participant}><Avatar name={participant} size="sm" /><div><strong>{participant === user?.id ? user.username : participant}</strong><span>{participant === user?.id ? 'You' : 'Online'}</span></div><span className="online-dot" /></div>)}</div><div className="collaboration-info"><span>Access</span><strong>{collaboration.role || '—'}</strong><span>Pending edits</span><strong>{collaboration.pendingCount}</strong></div></aside>
      </div>
      <footer className="editor-statusbar"><div><span className={`status-indicator status-${collaboration.connection.toLowerCase()}`} /> {collaboration.connection.replace('_', ' ').toLowerCase()}{collaboration.transport && <span> · {collaboration.transport}</span>}</div><div><span>Version {collaboration.version}</span><span>{collaboration.pendingCount} pending</span><span>{collaboration.synchronized ? 'Synced' : 'Waiting for sync'}</span><span>{language}</span></div></footer>
      {shareOpen && <ShareDialog documentId={decodedId} name={fileName} onClose={() => setShareOpen(false)} />}
      {historyOpen && <DocumentHistory key={decodedId} id={decodedId} current={collaboration.content} version={collaboration.version} onClose={() => setHistoryOpen(false)} />}
    </div>
  )
}
