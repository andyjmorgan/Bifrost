import { useEffect, useState, type FormEvent } from 'react'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { Button } from '../../ui/components/button'
import { Callout } from '../Callout'
import { api, errorMessage, type CreateServerResult, type ServerDTO } from '../../api'
import { AuthFields, emptyAuthDraft, toAuthInput, validateAuth, type AuthDraft } from './AuthFields'

/** Replace a server's credential, possibly switching the auth kind. */
export function CredentialDialog({
  open,
  server,
  onClose,
  onSaved,
}: {
  open: boolean
  server: ServerDTO
  onClose: () => void
  onSaved: (result: CreateServerResult) => void
}) {
  const [draft, setDraft] = useState<AuthDraft>(() => emptyAuthDraft(server.authKind))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    if (open) {
      setError(undefined)
      const d = emptyAuthDraft(server.authKind)
      if (server.credential?.kind === 'basic' && server.credential.basicUsername) d.username = server.credential.basicUsername
      if (server.credential?.kind === 'header' && server.credential.headerName) d.headerName = server.credential.headerName
      if (server.credential?.kind === 'oauth' && server.credential.oauthScopes?.length) d.scopes = server.credential.oauthScopes.join(' ')
      setDraft(d)
    }
  }, [open, server])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const err = validateAuth(draft)
    if (err) return setError(err)
    setBusy(true)
    setError(undefined)
    try {
      onSaved(await api.servers.putCredential(server.id, toAuthInput(draft)))
    } catch (e2) {
      setError(errorMessage(e2))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-w-xl">
        <form className="contents" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Replace credential</DialogTitle>
            <DialogDescription>The stored secret for {server.name} is overwritten. Choosing OAuth starts a new consent flow.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <AuthFields value={draft} onChange={setDraft} disabled={busy} />
            {error && <Callout tone="danger">{error}</Callout>}
          </DialogBody>
          <DialogFooter>
            <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Saving…' : draft.kind === 'oauth' ? 'Save and authorize' : 'Save credential'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
