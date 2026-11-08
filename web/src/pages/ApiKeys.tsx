import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { KeyRound, KeySquare, Plus, Trash2 } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Checkbox } from '../ui/components/checkbox'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/components/dialog'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary, RowActions } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { FormField } from '../components/FormField'
import { CopyButton } from '../components/CopyButton'
import { ScopeBadges } from '../components/ScopeBadges'
import { getConfig } from '../auth'
import { api, errorMessage, type APIKeyDTO, type CreateAPIKeyResult } from '../api'
import { absoluteTime, cn, relativeTime } from '../lib'

const SCOPE_HINTS: Record<string, string> = {
  'api:read': 'Read servers, keys and the audit trail.',
  'api:edit': 'Manage API keys.',
  'mcp:edit': 'Add, edit and remove MCP servers and their credentials.',
  'mcp:execute': 'Call /mcp and /{slug}/mcp through the gateway.',
}
const DEFAULT_SCOPES = ['api:read', 'api:edit', 'mcp:execute', 'mcp:edit']

const COLS = 'minmax(0,1.5fr) minmax(0,1.4fr) 130px 130px 110px 56px'

export function ApiKeysPage() {
  const [keys, setKeys] = useState<APIKeyDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [open, setOpen] = useState(false)
  const [created, setCreated] = useState<CreateAPIKeyResult>()
  const [revoking, setRevoking] = useState<APIKeyDTO>()
  const [revokeBusy, setRevokeBusy] = useState(false)
  const [revokeErr, setRevokeErr] = useState<string>()

  const load = useCallback(
    () =>
      api.apiKeys
        .list()
        .then((items) => {
          setKeys(items)
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  const revoke = async () => {
    if (!revoking) return
    setRevokeBusy(true)
    setRevokeErr(undefined)
    try {
      await api.apiKeys.revoke(revoking.id)
      toast.success(`Revoked ${revoking.name}`)
      setRevoking(undefined)
      void load()
    } catch (e) {
      setRevokeErr(errorMessage(e))
    } finally {
      setRevokeBusy(false)
    }
  }

  const active = keys.filter((k) => !k.revokedAt).length

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Access"
        title="API keys"
        description="Keys agents present to the gateway's MCP endpoints and REST API. Each acts as you, limited to the scopes you grant it."
        actions={
          <Button variant="primary" onClick={() => setOpen(true)}>
            <Plus /> New API key
          </Button>
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <ListCard>
        <ListBar>
          <span>
            {keys.length.toLocaleString()} {keys.length === 1 ? 'key' : 'keys'}
            {keys.length !== active ? ` · ${keys.length - active} revoked` : ''}
          </span>
        </ListBar>
        {keys.length > 0 && (
          <ListHead cols={COLS}>
            <span>Key</span>
            <span>Scopes</span>
            <span>Created</span>
            <span>Last used</span>
            <span>Status</span>
            <span />
          </ListHead>
        )}
        {keys.length === 0 ? (
          <ListEmpty
            icon={KeySquare}
            title={loaded ? 'No API keys yet' : 'Loading…'}
            hint={loaded ? 'Create one with mcp:execute to let an agent call the gateway, or api:read to browse it.' : undefined}
            action={
              loaded ? (
                <Button variant="secondary" onClick={() => setOpen(true)}>
                  <Plus /> New API key
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ListRows>
            {keys.map((k) => {
              const revoked = !!k.revokedAt
              return (
                <ListRow key={k.id} cols={COLS} className={cn(revoked && 'opacity-60')}>
                  <div className="flex min-w-0 items-center gap-3">
                    <IconTile icon={KeyRound} tone={revoked ? 'muted' : 'info'} />
                    <Primary title={k.name} subtitle={`${k.prefix}…`} mono />
                  </div>
                  <Cell label="Scopes">
                    <ScopeBadges scopes={k.scopes} />
                  </Cell>
                  <Cell label="Created">
                    <span className="block truncate font-mono text-xs text-muted-foreground" title={absoluteTime(k.createdAt)}>
                      {relativeTime(k.createdAt)}
                    </span>
                  </Cell>
                  <Cell label="Last used">
                    <span className="block truncate font-mono text-xs text-muted-foreground" title={absoluteTime(k.lastUsedAt)}>
                      {k.lastUsedAt ? relativeTime(k.lastUsedAt) : 'never'}
                    </span>
                  </Cell>
                  <Cell label="Status">
                    {revoked ? (
                      <StatusPill tone="danger">
                        <span title={absoluteTime(k.revokedAt)}>Revoked</span>
                      </StatusPill>
                    ) : (
                      <StatusPill tone="success">Active</StatusPill>
                    )}
                  </Cell>
                  <RowActions>
                    {!revoked && (
                      <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Revoke" title="Revoke" onClick={() => setRevoking(k)}>
                        <Trash2 />
                      </Button>
                    )}
                  </RowActions>
                </ListRow>
              )
            })}
          </ListRows>
        )}
      </ListCard>

      <NewKeyDialog
        open={open}
        onClose={() => setOpen(false)}
        onCreated={(r) => {
          setOpen(false)
          setCreated(r)
          void load()
        }}
      />
      <SecretDialog created={created} onClose={() => setCreated(undefined)} />
      <ConfirmDialog
        open={!!revoking}
        title={`Revoke ${revoking?.name ?? 'key'}?`}
        description="Requests using this key are rejected immediately. This cannot be undone."
        confirmLabel="Revoke"
        busy={revokeBusy}
        error={revokeErr}
        onCancel={() => {
          setRevoking(undefined)
          setRevokeErr(undefined)
        }}
        onConfirm={revoke}
      />
    </div>
  )
}

function NewKeyDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (r: CreateAPIKeyResult) => void }) {
  const allScopes = getConfig()?.apiKeyScopes?.length ? getConfig()!.apiKeyScopes : DEFAULT_SCOPES
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    if (open) {
      setName('')
      setScopes(['mcp:execute'])
      setError(undefined)
    }
  }, [open])

  const toggle = (s: string, on: boolean) => setScopes((list) => (on ? [...new Set([...list, s])] : list.filter((x) => x !== s)))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return setError('Name is required.')
    if (!scopes.length) return setError('Pick at least one scope.')
    setBusy(true)
    setError(undefined)
    try {
      onCreated(await api.apiKeys.create({ name: name.trim(), scopes }))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <form className="contents" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>New API key</DialogTitle>
            <DialogDescription>The secret is shown once, right after creation. Name it after the agent or host that will use it.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <FormField label="Name" required>
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="claude-desktop" autoFocus required />
            </FormField>
            <FormField label="Scopes" required>
              <div className="divide-y divide-border rounded-lg border border-border">
                {allScopes.map((s) => {
                  const id = `scope-${s.replace(/[^a-z]/g, '-')}`
                  return (
                    <label key={s} htmlFor={id} className="flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors hover:bg-elevated/60">
                      <Checkbox id={id} className="mt-0.5" checked={scopes.includes(s)} onCheckedChange={(v) => toggle(s, v === true)} />
                      <span className="min-w-0">
                        <span className="block font-mono text-xs font-medium text-foreground">{s}</span>
                        {SCOPE_HINTS[s] && <span className="mt-0.5 block text-xs leading-5 text-muted-foreground">{SCOPE_HINTS[s]}</span>}
                      </span>
                    </label>
                  )
                })}
              </div>
            </FormField>
            {error && <Callout tone="danger">{error}</Callout>}
          </DialogBody>
          <DialogFooter>
            <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Creating…' : 'Create key'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function SecretDialog({ created, onClose }: { created?: CreateAPIKeyResult; onClose: () => void }) {
  return (
    <Dialog open={!!created} onOpenChange={(o) => !o && onClose()}>
      <DialogContent onOpenAutoFocus={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>Key created</DialogTitle>
          <DialogDescription>Copy the secret now. It is not stored and cannot be shown again.</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {created && (
            <>
              <div>
                <p className="text-xs font-semibold text-muted-foreground">{created.key.name}</p>
                <div className="mt-1.5 flex items-start gap-2 rounded-lg border border-dashed border-border-strong bg-code p-3">
                  <code className="min-w-0 flex-1 break-all font-mono text-xs leading-5 text-code-foreground">{created.secret}</code>
                  <CopyButton value={created.secret} label="Copy secret" />
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span>Scopes</span>
                <ScopeBadges scopes={created.key.scopes} max={6} />
              </div>
              <Callout tone="info">
                Send it as <code className="font-mono text-xs">Authorization: Bearer …</code> to the gateway's MCP endpoints and REST API.
              </Callout>
            </>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
