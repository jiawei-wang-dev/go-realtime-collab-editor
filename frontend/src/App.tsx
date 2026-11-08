import { Navigate, Outlet, Route, Routes } from 'react-router-dom'
import { LoadingState } from './components/Feedback'
import { useAuthBootstrap } from './hooks/useAuthBootstrap'
import { AuthPage } from './pages/AuthPage'
import { DashboardPage } from './pages/DashboardPage'
import { EditorPage } from './pages/EditorPage'
import { SettingsPage } from './pages/SettingsPage'
import { useAuthStore } from './stores/authStore'

function ProtectedRoute() {
  const phase = useAuthStore((state) => state.phase)
  if (phase === 'BOOTSTRAPPING') return <div className="boot-screen"><LoadingState label="Restoring your session…" /></div>
  if (phase !== 'AUTHENTICATED') return <Navigate to="/auth" replace />
  return <Outlet />
}

export function App() {
  useAuthBootstrap()
  return (
    <Routes>
      <Route path="/auth" element={<AuthPage />} />
      <Route element={<ProtectedRoute />}>
        <Route index element={<DashboardPage />} />
        <Route path="documents/:documentId" element={<EditorPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
