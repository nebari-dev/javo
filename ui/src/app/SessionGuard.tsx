import { useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { isUnauthorized } from '../lib/http'
import type { LoginState } from '../features/auth/Login'

/**
 * Sends the user to sign in, saying the session ended, when any protected
 * request answers 401 (on first load too), keeping the page they were on
 * in next. Sign-in and sign-out mutations (meta.authFlow) are left alone.
 */
export function SessionGuard() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const loc = useLocation()
  const here = loc.pathname + loc.search

  useEffect(() => {
    const go = (err: unknown) => {
      if (!isUnauthorized(err)) return
      const state: LoginState = { expired: true }
      navigate(`/login?next=${encodeURIComponent(here)}`, { replace: true, state })
    }
    const unsubQ = qc.getQueryCache().subscribe((e) => {
      if (e.type === 'updated' && e.action.type === 'error') go(e.action.error)
    })
    const unsubM = qc.getMutationCache().subscribe((e) => {
      if (e.type === 'updated' && e.action.type === 'error' && !e.mutation.meta?.authFlow) go(e.action.error)
    })
    // A 401 that happened before this mounted (first render) is in the cache already.
    for (const q of qc.getQueryCache().getAll()) if (isUnauthorized(q.state.error)) go(q.state.error)
    return () => {
      unsubQ()
      unsubM()
    }
  }, [qc, navigate, here])
  return null
}
