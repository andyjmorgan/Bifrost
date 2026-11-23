import { useEffect, useState, type FormEvent } from 'react'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { Button } from '../../ui/components/button'
import { Callout } from '../Callout'
import { api, errorMessage, type CreateServerResult, type ServerDTO, type UserDTO } from '../../api'
import { useMe } from '../../me'
import { AuthFields, emptyAuthDraft, toAuthInput, validateAuth, type AuthDraft } from './AuthFields'
import { isKeyringKind, keyringCredentialName, keyringOwnerPhrase, pickOfCredential } from './credential'

/** Set or replace a server's credential, possibly switching the auth kind. */
export function CredentialDialog({
  open,
  server,
  users = [],
  onClose,
  onSaved,
}: {
  open: boolean
  server: ServerDTO
  /** Names the owner of a keyring credential that is somebody else's. */
  users?: UserDTO[]
  onClose: () => void
  onSaved: (result: CreateServerResult) => void
}) {
  const { userId } = useMe()
  const [draft, setDraft] = useState<AuthDraft>(() => emptyAuthDraft(server.authKind))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  const cred = server.credential
  const fresh = server.authKind === 'none'
  // A keyring credential in somebody else's keyring is not in the caller's list, so say what it is.
  const othersKeyring = !!cred && isKeyringKind(cred.kind) && cred.keyringOwner !== userId
  const keyringNote = othersKeyring
    ? `This server uses ${keyringCredentialName(cred)} from ${keyringOwnerPhrase(cred, userId, users)}. Saving points it at a credential from yours instead.`
    : undefined

  useEffect(() => {
    if (open) {
      setError(undefined)
      const d = emptyAuthDraft(server.authKind)
      const c = server.credential
      if (c?.kind === 'basic' && c.basicUsername) d.username = c.basicUsername
      if (c?.kind === 'header' && c.headerName) d.headerName = c.headerName
      if (c?.kind === 'oauth' && c.oauthScopes?.length) d.scopes = c.oauthScopes.join(' ')
      // Preselect the current keyring credential only when it is the caller's to pick again.
      if (c && isKeyringKind(c.kind) && c.keyringOwner === userId) d.keyring = pickOfCredential(c)
      setDraft(d)
    }
  }, [open, server, userId])

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
            <DialogTitle>{fresh ? 'Set credential' : 'Replace credential'}</DialogTitle>
            <DialogDescription>
              {fresh
                ? `Choose how the gateway authenticates to ${server.name}. Choosing OAuth starts a consent flow.`
                : `The credential for ${server.name} is replaced. Choosing OAuth starts a new consent flow.`}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <AuthFields value={draft} onChange={setDraft} disabled={busy} keyringNote={keyringNote} />
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
