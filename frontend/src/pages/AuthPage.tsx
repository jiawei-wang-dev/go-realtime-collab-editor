import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { Brand } from '../components/Brand'
import { ErrorBanner } from '../components/Feedback'
import { useAuthStore } from '../stores/authStore'

export function AuthPage() {
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const phase = useAuthStore((state) => state.phase)
  const loading = useAuthStore((state) => state.loading)
  const error = useAuthStore((state) => state.error)
  const clearError = useAuthStore((state) => state.clearError)
  const login = useAuthStore((state) => state.login)
  const register = useAuthStore((state) => state.register)
  const navigate = useNavigate()

  if (phase === 'AUTHENTICATED') return <Navigate to="/" replace />

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    try {
      if (mode === 'login') await login(username.trim(), password)
      else await register(username.trim(), password)
      navigate('/', { replace: true })
    } catch {
      // Store owns the client-safe error message.
    }
  }

  const changeMode = (next: 'login' | 'register') => {
    clearError()
    setMode(next)
  }

  return (
    <div className="auth-page">
      <div className="auth-atmosphere" />
      <section className="auth-intro">
        <Brand />
        <div className="auth-copy">
          <span className="eyebrow">DISTRIBUTED BY DESIGN</span>
          <h1>Code together.<br /><em>Stay in sync.</em></h1>
          <p>A focused collaborative editor backed by versioned OT, durable recovery, and multi-node ownership.</p>
          <div className="signal-card">
            <span className="signal-dot" />
            <div><strong>Realtime workspace</strong><span>QUIC + WebSocket gateway</span></div>
            <code>v12 · synced</code>
          </div>
        </div>
        <p className="auth-footnote">Secure sessions · Durable edits · Automatic recovery</p>
      </section>
      <section className="auth-panel">
        <div className="auth-form-wrap">
          <div className="auth-mobile-brand"><Brand /></div>
          <div className="auth-tabs" role="tablist">
            <button className={mode === 'login' ? 'active' : ''} onClick={() => changeMode('login')}>Sign in</button>
            <button className={mode === 'register' ? 'active' : ''} onClick={() => changeMode('register')}>Create account</button>
          </div>
          <div className="auth-heading">
            <h2>{mode === 'login' ? 'Welcome back' : 'Create your workspace'}</h2>
            <p>{mode === 'login' ? 'Continue to your collaborative documents.' : 'Choose a username and start editing together.'}</p>
          </div>
          <ErrorBanner message={error} onDismiss={clearError} />
          <form onSubmit={(event) => void submit(event)} className="auth-form">
            <label>Username<input autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} minLength={3} maxLength={32} pattern={'[A-Za-z0-9_.\\x2d]+'} required placeholder="your.name" /></label>
            <label>Password<input type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} value={password} onChange={(event) => setPassword(event.target.value)} minLength={8} maxLength={72} required placeholder="At least 8 characters" /></label>
            <button className="primary-button auth-submit" disabled={loading || phase === 'BOOTSTRAPPING'}>
              {loading ? <><span className="spinner" /> Please wait</> : mode === 'login' ? 'Sign in' : 'Create account'}
            </button>
          </form>
          <p className="auth-security">Refresh credentials stay in a secure HttpOnly cookie and are never written to browser storage.</p>
        </div>
      </section>
    </div>
  )
}
