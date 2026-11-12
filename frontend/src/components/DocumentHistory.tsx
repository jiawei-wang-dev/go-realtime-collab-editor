import { useEffect, useRef, useState } from 'react'
import { getHistoricalVersion, listHistory, type HistoryEntry } from '../api/documents'
import { ErrorBanner } from './Feedback'

export function DocumentHistory({ id, current, version, onClose }: {
  id: string; current: string; version: number; onClose: () => void
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const request = useRef(0)
  const [entries, setEntries] = useState<HistoryEntry[]>([])
  const [selected, setSelected] = useState<{ version: number; content: string }>()
  const [more, setMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const load = async (before?: number) => {
    setLoading(true)
    setError('')
    try {
      const page = await listHistory(id, before)
      setEntries((previous) => before === undefined ? page : [...previous, ...page])
      setMore(page.length === 30 && page.at(-1)?.version !== 0)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Unable to load history.')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    dialog.current?.showModal()
    void load()
  }, [])

  const select = async (target: number) => {
    const sequence = ++request.current
    setError('')
    try {
      const document = await getHistoricalVersion(id, target)
      if (sequence === request.current) setSelected(document)
    } catch (reason) {
      if (sequence === request.current) setError(reason instanceof Error ? reason.message : 'Unable to read this version.')
    }
  }

  return <dialog ref={dialog} className="modal workspace-dialog document-history" aria-labelledby="document-history-title" onClose={onClose}>
    <h2 id="document-history-title">Version history</h2>
    <ErrorBanner message={error} onDismiss={() => setError('')} />
    <div className="document-history-layout">
      <div>
        <ul className="quick-open-results">
          {entries.map((entry) => <li key={entry.version}><button type="button" className="quick-open-result" aria-current={selected?.version === entry.version} onClick={() => void select(entry.version)}>
            Version {entry.version}{entry.kind ? ` · ${entry.kind}` : ''}
          </button></li>)}
        </ul>
        {loading && <p role="status">Loading versions…</p>}
        {more && <button type="button" disabled={loading} className="ghost-button" onClick={() => void load(entries.at(-1)?.version)}>Older versions</button>}
      </div>
      {selected ? <div className="document-history-comparison">
        <section><h3>Version {selected.version}</h3><pre>{selected.content}</pre></section>
        <section><h3>Current editor · Version {version}</h3><pre>{current}</pre></section>
      </div> : <p>Select a version to compare it with the current editor.</p>}
    </div>
    <div className="modal-actions"><button type="button" className="secondary-button" onClick={onClose}>Done</button></div>
  </dialog>
}
