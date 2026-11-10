import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { RunConsole } from './RunConsole'

describe('RunConsole', () => {
  it('keeps stdout and stderr distinct and shows execution metadata', () => {
    render(<RunConsole result={{ stdout: 'hello\n', stderr: 'warning\n', exit_code: 1, duration_ms: 42, timed_out: false, output_truncated: true }} error="" running={false} onClear={vi.fn()} />)
    expect(screen.getByText('hello')).toHaveClass('run-stdout')
    expect(screen.getByText('warning')).toHaveClass('run-stderr')
    expect(screen.getByText('Exited 1')).toBeInTheDocument()
    expect(screen.getByText('42 ms')).toBeInTheDocument()
    expect(screen.getByText('output truncated')).toBeInTheDocument()
  })

  it('reports a timeout', () => {
    render(<RunConsole result={{ stdout: '', stderr: '', exit_code: -1, duration_ms: 10_000, timed_out: true, output_truncated: false }} error="" running={false} onClear={vi.fn()} />)
    expect(screen.getByText('Timed out')).toBeInTheDocument()
  })
})
