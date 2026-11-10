import type { RunResult } from '../types/api'

interface RunConsoleProps {
  result: RunResult | null
  error: string
  running: boolean
  onClear: () => void
}

export function RunConsole({ result, error, running, onClear }: RunConsoleProps) {
  const status = running
    ? 'Running…'
    : error
      ? 'Request failed'
      : result?.timed_out
        ? 'Timed out'
        : result
          ? `Exited ${result.exit_code}`
          : 'Ready'

  return (
    <section className="run-console" aria-label="Program output">
      <header>
        <strong>OUTPUT</strong>
        <div>
          <span className={error || result?.exit_code ? 'run-status is-error' : 'run-status'}>{status}</span>
          {result && <span>{result.duration_ms} ms</span>}
          {result?.output_truncated && <span>output truncated</span>}
          <button type="button" onClick={onClear} aria-label="Clear output">Clear</button>
        </div>
      </header>
      <div className="run-console-content" aria-live="polite">
        {running && <p className="run-console-placeholder">Compiling and running in an isolated container…</p>}
        {error && <pre className="run-stderr">{error}</pre>}
        {!running && result?.stdout && <pre className="run-stdout">{result.stdout}</pre>}
        {!running && result?.stderr && <pre className="run-stderr">{result.stderr}</pre>}
        {!running && result && !result.stdout && !result.stderr && <p className="run-console-placeholder">Program completed without output.</p>}
      </div>
    </section>
  )
}
