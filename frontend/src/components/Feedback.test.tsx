import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { EmptyState, ErrorBanner, LoadingState } from './Feedback'

describe('feedback components', () => {
  it('renders loading and empty states with accessible copy', () => {
    const { rerender } = render(<LoadingState label="Loading documents" />)
    expect(screen.getByText('Loading documents')).toBeInTheDocument()

    rerender(<EmptyState title="No documents" description="Create the first document." />)
    expect(screen.getByRole('heading', { name: 'No documents' })).toBeInTheDocument()
    expect(screen.getByText('Create the first document.')).toBeInTheDocument()
  })

  it('dismisses an API error', async () => {
    const dismiss = vi.fn()
    render(<ErrorBanner message="Session expired" onDismiss={dismiss} />)

    expect(screen.getByRole('alert')).toHaveTextContent('Session expired')
    await userEvent.click(screen.getByRole('button', { name: 'Dismiss error' }))
    expect(dismiss).toHaveBeenCalledOnce()
  })
})
