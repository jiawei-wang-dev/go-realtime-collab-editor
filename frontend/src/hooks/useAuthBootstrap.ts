import { useEffect } from 'react'
import { useAuthStore } from '../stores/authStore'

export function useAuthBootstrap(): void {
  const phase = useAuthStore((state) => state.phase)
  const bootstrap = useAuthStore((state) => state.bootstrap)

  useEffect(() => {
    if (phase === 'BOOTSTRAPPING') {
      void bootstrap()
    }
  }, [bootstrap, phase])

  useEffect(() => {
    const expire = () => useAuthStore.getState().expire()
    window.addEventListener('collab:session-expired', expire)
    return () => window.removeEventListener('collab:session-expired', expire)
  }, [])
}
