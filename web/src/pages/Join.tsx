import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { toast } from 'sonner'
import { Building2 } from 'lucide-react'
import { Logo } from '../components/Logo'
import { Callout } from '../components/Callout'
import { StatusPill } from '../components/StatusPill'
import { Button } from '../ui/components/button'
import { DEFAULT_ROUTE } from '../auth'
import { ApiError, api, errorMessage, type InvitePreviewDTO } from '../api'
import { absoluteTime, relativeTime } from '../lib'

const linkClass = 'text-sm underline decoration-accent/60 underline-offset-[3px] hover:text-accent'

/**
 * /join/:token — the landing page of an invite link. Rendered outside the app shell (like the
 * sign-in screen) and behind the auth gate, which remembers this path across the IdP round-trip.
 */
export function JoinPage() {
  const { token = '' } = useParams()
  const navigate = useNavigate()
  const [preview, setPreview] = useState<InvitePreviewDTO>()
  const [invalid, setInvalid] = useState(false)
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.org.invites
      .preview(token)
      .then((p) => !cancelled && setPreview(p))
      .catch((e) => {
        if (cancelled) return
        if (e instanceof ApiError && e.status === 404) setInvalid(true)
        else setError(errorMessage(e))
      })
    return () => {
      cancelled = true
    }
  }, [token])

  // Joining when already a member is a no-op insert that just makes the org active, which is
  // exactly "switch to it" (the preview carries no org id for /org/switch).
  const join = async () => {
    setBusy(true)
    setError(undefined)
    try {
      const org = await api.org.join(token)
      toast.success(preview?.member ? `Switched to ${org.name}` : `Welcome to ${org.name}`)
      navigate(DEFAULT_ROUTE, { replace: true })
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setInvalid(true)
      else setError(errorMessage(e))
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-dvh flex-col bg-background text-foreground">
      <main className="flex flex-1 items-center justify-center p-4">
        <div className="w-full max-w-sm space-y-6 text-center">
          <div className="flex justify-center">
            <Logo compact imageClassName="size-16" />
          </div>
          {invalid ? (
            <>
              <div className="space-y-2">
                <h1 className="text-2xl font-semibold tracking-tight">This invite is invalid, used or expired</h1>
                <p className="text-sm text-muted-foreground">Invite links work once and expire after a while. Ask an admin of the org for a new one.</p>
              </div>
              <Button variant="secondary" asChild>
                <Link to={DEFAULT_ROUTE}>Go to Bifrost</Link>
              </Button>
            </>
          ) : preview ? (
            <>
              <div className="space-y-3">
                <p className="text-xs font-semibold uppercase tracking-[0.2em] text-accent">{preview.member ? 'Already a member' : 'You’re invited'}</p>
                <h1 className="text-3xl font-semibold tracking-tight">{preview.orgName}</h1>
                <p className="text-sm text-muted-foreground">
                  {preview.member
                    ? `You already belong to ${preview.orgName}. Switch to it to see its servers and policies.`
                    : `Join ${preview.orgName} to share its MCP servers, policies and audit trail. Your personal org stays yours.`}
                </p>
              </div>
              <div className="flex flex-wrap items-center justify-center gap-2 text-xs text-muted-foreground">
                <Building2 className="size-3.5" />
                <span>Role</span>
                <StatusPill tone={preview.role === 'admin' ? 'accent' : 'muted'}>{preview.role === 'admin' ? 'Admin' : 'Member'}</StatusPill>
                {!preview.member && (
                  <span className="font-mono" title={absoluteTime(preview.expiresAt)}>
                    · expires {relativeTime(preview.expiresAt)}
                  </span>
                )}
              </div>
              {error && (
                <Callout tone="danger" className="text-left">
                  {error}
                </Callout>
              )}
              <div className="flex flex-col items-center gap-3">
                <Button variant="primary" className="w-full" onClick={join} disabled={busy}>
                  {busy ? 'Joining…' : preview.member ? `Switch to ${preview.orgName}` : `Join ${preview.orgName}`}
                </Button>
                <Link to={DEFAULT_ROUTE} className={linkClass}>
                  Not now
                </Link>
              </div>
            </>
          ) : error ? (
            <>
              <Callout tone="danger" className="text-left">
                {error}
              </Callout>
              <Link to={DEFAULT_ROUTE} className={linkClass}>
                Go to Bifrost
              </Link>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">Checking your invite…</p>
          )}
        </div>
      </main>
      <footer className="p-4 text-center text-sm text-muted-foreground">Built with questionable decisions and caffeine</footer>
    </div>
  )
}
