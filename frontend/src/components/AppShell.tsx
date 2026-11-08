import type { ReactNode } from 'react'
import { NavLink, useNavigate } from 'react-router-dom'
import { useAuthStore } from '../stores/authStore'
import { Avatar } from './Avatar'
import { Brand } from './Brand'
import { Icon } from './Icon'

export function AppShell({ children }: { children: ReactNode }) {
  const user = useAuthStore((state) => state.user)
  const logout = useAuthStore((state) => state.logout)
  const navigate = useNavigate()

  const signOut = async () => {
    await logout()
    navigate('/auth', { replace: true })
  }

  return (
    <div className="app-shell">
      <aside className="app-sidebar">
        <Brand />
        <nav className="sidebar-nav" aria-label="Main navigation">
          <NavLink to="/" end><Icon name="files" /><span>Documents</span></NavLink>
          <NavLink to="/settings"><Icon name="settings" /><span>Settings</span></NavLink>
        </nav>
        <div className="sidebar-user">
          <Avatar name={user?.username ?? 'User'} />
          <div><strong>{user?.username ?? 'User'}</strong><span>Online</span></div>
          <button className="icon-button" onClick={() => void signOut()} title="Sign out"><Icon name="logout" /></button>
        </div>
      </aside>
      <main className="app-main">{children}</main>
    </div>
  )
}
