import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { toast } from 'sonner'
import { KeyRound, KeySquare, Plus, Trash2 } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Checkbox } from '../../ui/components/checkbox'
import { Switch } from '../../ui/components/switch'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { PageHeader } from '../../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary, RowActions } from '../../components/ListCard'
import { IconTile } from '../../components/IconTile'
import { StatusPill } from '../../components/StatusPill'
import { Callout } from '../../components/Callout'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { FormField } from '../../components/FormField'
import { CopyButton } from '../../components/CopyButton'
import { ScopeBadges } from '../../components/ScopeBadges'
import { tabPanelProps } from '../../components/Tabs'
import { errorMessage } from '../../api'
import { keyring, type AccessKey, type AccessKeyScope, type CreatedAccessKey } from '../../api/keyring'
import { absoluteTime, cn, relativeTime } from '../../lib'

const SCOPES: { value: AccessKeyScope; hint: string }[] = [
  { value: 'vault:read', hint: 'Read secrets, OAuth tokens and provider config.' },
  { value: 'vault:readwrite', hint: 'Read, plus create, update and delete secrets.' },
  { value: 'vault:audit', hint: 'Read the keyring audit trail.' },
]

const COLS = 'minmax(0,1.5fr) minmax(0,1.3fr) 130px 130px 110px 96px'

/**
 * The keyring's own access keys (dwv_…): what the CLI and agents present to the keyring in place
 * of a browser session. The second tab of the API keys page; `tabs` is that page's tab strip.
 */
export function KeyringAccessKeys({ tabs }: { tabs: ReactNode }) {
  const [keys, setKeys] = useState<AccessKey[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [open, setOpen] = useState(false)
  const [created, setCreated] = useState<CreatedAccessKey>()
  const [revoking, setRevoking] = useState<AccessKey>()
  const [revokeBusy, setRevokeBusy] = useState(false)
  const [revokeErr, setRevokeErr] = useState<string>()

  const load = useCallback(
    () =>
      keyring.accessKeys
        .list()
        .then((items) => {
          setKeys(items ?? [])
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  const toggle = async (k: AccessKey) => {
    try {
      await keyring.accessKeys.setEnabled(k.id, !k.enabled)
      toast.success(`${k.enabled ? 'Disabled' : 'Enabled'} ${k.name}`)
    } catch (e) {
      toast.error('Could not update the key', { description: errorMessage(e) })
    }
    void load()
  }

  const revoke = async () => {
    if (!revoking) return
    setRevokeBusy(true)
    setRevokeErr(undefined)
    try {
      await keyring.accessKeys.remove(revoking.id)
      toast.success(`Revoked ${revoking.name}`)
      setRevoking(undefined)
      void load()
    } catch (e) {
      setRevokeErr(errorMessage(e))
    } finally {
      setRevokeBusy(false)
    }
  }

  const disabled = keys.filter((k) => !k.enabled).length

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Access"
        title="API keys"
        description="Keyring access keys (dwv_…) let the CLI and agents read secrets and OAuth tokens. They are not gateway keys: neither kind opens the other's API. Each acts as you, limited to its scopes."
        actions={
          <Button variant="primary" onClick={() => setOpen(true)}>
            <Plus /> New access key
          </Button>
        }
      />

      {tabs}

      {error && <Callout tone="danger">{error}</Callout>}

      <ListCard {...tabPanelProps('keyring')}>
        <ListBar>
          <span>
            {keys.length.toLocaleString()} {keys.length === 1 ? 'access key' : 'access keys'}
            {disabled ? ` · ${disabled} disabled` : ''}
          </span>
        </ListBar>
        {keys.length > 0 && (
          <ListHead cols={COLS}>
            <span>Access key</span>
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
            title={loaded ? 'No keyring access keys yet' : 'Loading…'}
            hint={loaded ? 'Create one to let the CLI or an agent call the keyring without a browser session.' : undefined}
            action={
              loaded ? (
                <Button variant="secondary" onClick={() => setOpen(true)}>
                  <Plus /> New access key
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ListRows>
            {keys.map((k) => (
              <ListRow key={k.id} cols={COLS} className={cn(!k.enabled && 'opacity-60')}>
                <div className="flex min-w-0 items-center gap-3">
                  <IconTile icon={KeyRound} tone={k.enabled ? 'accent' : 'muted'} />
                  <Primary
                    title={
                      <span className="block truncate" title={k.description || undefined}>
                        {k.name}
                      </span>
                    }
                    subtitle={`${k.prefix}…`}
                    mono
                  />
                </div>
                <Cell label="Scopes">
                  <ScopeBadges scopes={k.scopes ?? []} />
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
                <Cell label="Status">{k.enabled ? <StatusPill tone="success">Active</StatusPill> : <StatusPill tone="muted">Disabled</StatusPill>}</Cell>
                <RowActions>
                  <Switch checked={k.enabled} onCheckedChange={() => toggle(k)} aria-label={`${k.name} enabled`} title={k.enabled ? 'Disable' : 'Enable'} />
                  <Button
                    variant="ghost"
                    size="iconSm"
                    className="hover:text-danger"
                    aria-label={`Revoke ${k.name}`}
                    title="Revoke"
                    onClick={() => {
                      setRevokeErr(undefined)
                      setRevoking(k)
                    }}
                  >
                    <Trash2 />
                  </Button>
                </RowActions>
              </ListRow>
            ))}
          </ListRows>
        )}
      </ListCard>

      <NewAccessKeyDialog
        open={open}
        onClose={() => setOpen(false)}
        onCreated={(r) => {
          setOpen(false)
          setCreated(r)
          void load()
        }}
      />
      <CreatedDialog created={created} onClose={() => setCreated(undefined)} />
      <ConfirmDialog
        open={!!revoking}
        title={`Revoke ${revoking?.name ?? 'access key'}?`}
        description={`Anything still using this key${revoking ? ` (${revoking.prefix}…)` : ''} is refused immediately. The key is deleted and cannot be restored; to pause it instead, switch it off.`}
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

function NewAccessKeyDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (r: CreatedAccessKey) => void }) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [scopes, setScopes] = useState<AccessKeyScope[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    if (open) {
      setName('')
      setDescription('')
      setScopes(['vault:read'])
      setError(undefined)
    }
  }, [open])

  const toggle = (s: AccessKeyScope, on: boolean) => setScopes((list) => (on ? [...new Set([...list, s])] : list.filter((x) => x !== s)))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return setError('Name is required.')
    if (!scopes.length) return setError('Pick at least one scope.')
    setBusy(true)
    setError(undefined)
    try {
      onCreated(await keyring.accessKeys.create({ name: name.trim(), description: description.trim() || undefined, scopes }))
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
            <DialogTitle>New keyring access key</DialogTitle>
            <DialogDescription>The secret is shown once, right after creation. Name it after what will hold it, and grant only the scopes it needs.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <FormField label="Name" required>
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="agent-bot" autoFocus required />
            </FormField>
            <FormField label="Description">
              <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What this key is for" />
            </FormField>
            <FormField label="Scopes" required>
              <div className="divide-y divide-border rounded-lg border border-border">
                {SCOPES.map((s) => {
                  const id = `keyring-scope-${s.value.replace(/[^a-z]/g, '-')}`
                  return (
                    <label key={s.value} htmlFor={id} className="flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors hover:bg-elevated/60">
                      <Checkbox id={id} className="mt-0.5" checked={scopes.includes(s.value)} onCheckedChange={(v) => toggle(s.value, v === true)} />
                      <span className="min-w-0">
                        <span className="block font-mono text-xs font-medium text-foreground">{s.value}</span>
                        <span className="mt-0.5 block text-xs leading-5 text-muted-foreground">{s.hint}</span>
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

function CreatedDialog({ created, onClose }: { created?: CreatedAccessKey; onClose: () => void }) {
  return (
    <Dialog open={!!created} onOpenChange={(o) => !o && onClose()}>
      <DialogContent onOpenAutoFocus={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>Access key created</DialogTitle>
          <DialogDescription>Copy the secret now. It is not stored and cannot be shown again.</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {created && (
            <>
              <div>
                <p className="text-xs font-semibold text-muted-foreground">{created.name}</p>
                <div className="mt-1.5 flex items-start gap-2 rounded-lg border border-dashed border-border-strong bg-code p-3">
                  <code className="min-w-0 flex-1 break-all font-mono text-xs leading-5 text-code-foreground">{created.secret}</code>
                  <CopyButton value={created.secret} label="Copy secret" />
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span>Scopes</span>
                <ScopeBadges scopes={created.scopes ?? []} max={6} />
              </div>
              <Callout tone="info">
                Hand it to the CLI as <code className="font-mono text-xs">VAULT_API_KEY</code>, or send it to the keyring as <code className="font-mono text-xs">X-Api-Key</code>. It
                does not open the gateway's MCP endpoints. If you lose it, revoke the key and create another.
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
