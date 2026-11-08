import { useNavigate } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { Avatar } from '../components/Avatar'
import { Icon } from '../components/Icon'
import { useAuthStore } from '../stores/authStore'

export function SettingsPage() {
  const user = useAuthStore((state) => state.user)
  const logout = useAuthStore((state) => state.logout)
  const navigate = useNavigate()

  return <AppShell><div className="settings-page"><header className="page-header"><div><span className="eyebrow">SETTINGS</span><h1>Account and session</h1><p>Review the identity currently connected to this workspace.</p></div></header><section className="settings-card"><div className="settings-profile"><Avatar name={user?.username ?? 'User'} size="lg" /><div><h2>{user?.username}</h2><p>{user?.id}</p></div><span className="status-chip"><span /> {user?.status ?? 'active'}</span></div><dl><div><dt>Authentication</dt><dd>JWT access token + rotating HttpOnly refresh cookie</dd></div><div><dt>Session protection</dt><dd>Redis revocation, operation leases, and cross-node reconciliation</dd></div><div><dt>Profile editing</dt><dd>Not available in the current backend API</dd></div></dl><button className="danger-button" onClick={() => void logout().then(() => navigate('/auth'))}><Icon name="logout" /> Sign out of this session</button></section></div></AppShell>
}
