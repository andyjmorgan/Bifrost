import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Logo } from '../components/Logo'
import { Callout } from '../components/Callout'
import { Button } from '../ui/components/button'
import { completeLogin } from '../auth'

/** /auth/callback: finish the OIDC code exchange, then land on the remembered route. */
export function AuthCallbackPage() {
  const navigate = useNavigate()
  const [error, setError] = useState<string>()
  useEffect(() => {
    let cancelled = false
    completeLogin()
      .then((to) => {
        if (!cancelled) navigate(to, { replace: true })
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      cancelled = true
    }
  }, [navigate])
  return (
    <div className="flex min-h-dvh items-center justify-center bg-background p-4 text-foreground">
      <div className="w-full max-w-sm space-y-6 text-center">
        <div className="flex justify-center">
          <Logo compact imageClassName="size-16" />
        </div>
        {error ? (
          <>
            <Callout tone="danger" className="text-left">
              Sign-in did not complete: {error}
            </Callout>
            <Button variant="outline" onClick={() => navigate('/', { replace: true })}>
              Back to sign in
            </Button>
          </>
        ) : (
          <p className="text-sm text-muted-foreground">Completing sign-in…</p>
        )}
      </div>
    </div>
  )
}
