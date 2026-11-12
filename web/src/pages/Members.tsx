import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { Link2, MailPlus, Pencil, Plus, Trash2, UserRound, Users } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Input } from '../ui/components/input'
import { Badge } from '../ui/components/badge'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/components/select'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../ui/components/dialog'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary, RowActions } from '../components/ListCard'
import { DefinitionList, Definition } from '../components/DefinitionList'
import { IconTile } from '../components/IconTile'
import { StatusPill } from '../components/StatusPill'
import { Callout } from '../components/Callout'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { FormField } from '../components/FormField'
import { CopyButton } from '../components/CopyButton'
import { Tabs, TabPanel, type Tab } from '../components/Tabs'
import { api, errorMessage, type InviteDTO, type MemberDTO, type OrgDTO, type OrgRole } from '../api'
import { useMe } from '../me'
import { absoluteTime, cn, relativeTime } from '../lib'

const MEMBER_COLS = 'minmax(0,1.6fr) minmax(0,1.4fr) 140px 130px 56px'
const INVITE_COLS = 'minmax(0,1.3fr) minmax(0,1fr) 130px 130px minmax(0,1.1fr) 56px'

type TabKey = 'members' | 'invites'

type InviteStatus = { key: 'open' | 'used' | 'expired'; label: string; tone: 'success' | 'muted' | 'warning' }

function inviteStatus(inv: InviteDTO, nameOf: (id?: string) => string): InviteStatus {
  if (inv.usedAt) return { key: 'used', label: `Used by ${nameOf(inv.usedBy)}`, tone: 'muted' }
  if (new Date(inv.expiresAt).getTime() <= Date.now()) return { key: 'expired', label: 'Expired', tone: 'warning' }
  return { key: 'open', label: 'Open', tone: 'success' }
}

function RolePill({ role }: { role: OrgRole }) {
  return <StatusPill tone={role === 'admin' ? 'accent' : 'muted'}>{role === 'admin' ? 'Admin' : 'Member'}</StatusPill>
}

function Time({ value }: { value?: string }) {
  return (
    <span className="block truncate font-mono text-xs text-muted-foreground" title={absoluteTime(value)}>
      {relativeTime(value)}
    </span>
  )
}

export function MembersPage() {
  const { isAdmin, refreshMe } = useMe()
  const [params, setParams] = useSearchParams()
  const [org, setOrg] = useState<OrgDTO>()
  const [invites, setInvites] = useState<InviteDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [actionError, setActionError] = useState<string>()
  const [renaming, setRenaming] = useState(false)
  const [inviting, setInviting] = useState(false)
  const [created, setCreated] = useState<InviteDTO>()
  const [removing, setRemoving] = useState<MemberDTO>()
  const [removeBusy, setRemoveBusy] = useState(false)
  const [removeErr, setRemoveErr] = useState<string>()
  const [revoking, setRevoking] = useState<InviteDTO>()
  const [revokeBusy, setRevokeBusy] = useState(false)
  const [revokeErr, setRevokeErr] = useState<string>()
  const [roleBusy, setRoleBusy] = useState<string>()

  // The org's own view of the caller's role is the freshest; /me covers the first paint.
  const admin = org ? org.role === 'admin' : isAdmin
  const tab: TabKey = admin && params.get('tab') === 'invites' ? 'invites' : 'members'
  const setTab = (k: TabKey) => setParams(k === 'members' ? {} : { tab: k }, { replace: true })

  const loadInvites = useCallback(() => api.org.invites.list().then(setInvites), [])
  const load = useCallback(async () => {
    try {
      const o = await api.org.get()
      setOrg(o)
      setError(undefined)
      if (o.role === 'admin') await loadInvites()
      else setInvites([])
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setLoaded(true)
    }
  }, [loadInvites])
  useEffect(() => {
    void load()
  }, [load])

  const nameOf = (userId?: string) => {
    if (!userId) return 'someone'
    const m = org?.members.find((x) => x.userId === userId)
    return m ? m.name || m.email || m.userId : userId
  }

  const changeRole = async (m: MemberDTO, role: OrgRole) => {
    if (role === m.role) return
    setRoleBusy(m.userId)
    setActionError(undefined)
    try {
      const o = await api.org.setRole(m.userId, role)
      setOrg(o)
      toast.success(`${m.name || m.email} is now ${role === 'admin' ? 'an admin' : 'a member'}`)
      if (m.self) await refreshMe()
    } catch (e) {
      setActionError(errorMessage(e))
      toast.error(errorMessage(e))
    } finally {
      setRoleBusy(undefined)
    }
  }

  const remove = async () => {
    if (!removing) return
    setRemoveBusy(true)
    setRemoveErr(undefined)
    try {
      const o = await api.org.removeMember(removing.userId)
      toast.success(`Removed ${removing.name || removing.email}`)
      setRemoving(undefined)
      if (o) setOrg(o)
      else {
        await refreshMe()
        void load()
      }
    } catch (e) {
      setRemoveErr(errorMessage(e))
    } finally {
      setRemoveBusy(false)
    }
  }

  const revoke = async () => {
    if (!revoking) return
    setRevokeBusy(true)
    setRevokeErr(undefined)
    try {
      await api.org.invites.revoke(revoking.id)
      toast.success('Invite revoked')
      setRevoking(undefined)
      void loadInvites()
    } catch (e) {
      setRevokeErr(errorMessage(e))
    } finally {
      setRevokeBusy(false)
    }
  }

  const members = org?.members ?? []
  const openInvites = invites.filter((i) => inviteStatus(i, nameOf).key === 'open').length
  const tabs: Tab<TabKey>[] = [
    { key: 'members', label: 'Members', count: members.length },
    ...(admin ? [{ key: 'invites' as const, label: 'Invites', count: openInvites }] : []),
  ]

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Access"
        title="Members"
        description={
          admin
            ? 'Who shares this organisation’s servers, entitlements and audit trail. Invite people with a one-time link.'
            : 'Who shares this organisation’s servers, entitlements and audit trail. Admins manage members and invites.'
        }
        actions={
          admin ? (
            <Button variant="primary" onClick={() => setInviting(true)}>
              <Plus /> Create invite
            </Button>
          ) : undefined
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <DefinitionList>
        <Definition label="Organisation">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{org?.name ?? (loaded ? '—' : 'Loading…')}</span>
            {org && <Badge variant={org.personal ? 'muted' : 'info'}>{org.personal ? 'personal' : 'shared'}</Badge>}
            {admin && org && (
              <Button variant="link" size="sm" onClick={() => setRenaming(true)}>
                <Pencil /> Rename
              </Button>
            )}
          </div>
        </Definition>
        <Definition label="Your role">{org ? <RolePill role={org.role} /> : '—'}</Definition>
        {org?.personal && (
          <Definition label="About">
            <span className="text-muted-foreground">Your personal org. Invite people to share it, or join someone else’s from an invite link.</span>
          </Definition>
        )}
      </DefinitionList>

      {actionError && <Callout tone="danger">{actionError}</Callout>}

      {tabs.length > 1 && <Tabs tabs={tabs} value={tab} onChange={setTab} label="Members and invites" />}

      {tab === 'members' && (
        <TabPanel tabKey="members">
          <ListCard>
            <ListBar>
              <span>
                {members.length.toLocaleString()} {members.length === 1 ? 'member' : 'members'}
                {members.length > 0 && ` · ${members.filter((m) => m.role === 'admin').length} admin`}
              </span>
            </ListBar>
            {members.length > 0 && (
              <ListHead cols={MEMBER_COLS}>
                <span>Name</span>
                <span>Email</span>
                <span>Role</span>
                <span>Joined</span>
                <span />
              </ListHead>
            )}
            {members.length === 0 ? (
              <ListEmpty icon={Users} title={loaded ? 'No members' : 'Loading…'} />
            ) : (
              <ListRows>
                {members.map((m) => (
                  <ListRow key={m.userId} cols={MEMBER_COLS}>
                    <div className="flex min-w-0 items-center gap-3">
                      <IconTile icon={UserRound} tone={m.self ? 'accent' : 'muted'} />
                      <Primary
                        title={
                          <span className="inline-flex max-w-full items-center gap-2">
                            <span className="truncate">{m.name || m.email || m.userId}</span>
                            {m.self && <Badge>you</Badge>}
                          </span>
                        }
                        subtitle={m.userId}
                        mono
                      />
                    </div>
                    <Cell label="Email">
                      <span className="block truncate text-sm text-muted-foreground">{m.email || '—'}</span>
                    </Cell>
                    <Cell label="Role">
                      {admin ? (
                        <Select value={m.role} onValueChange={(v) => void changeRole(m, v as OrgRole)} disabled={roleBusy === m.userId}>
                          <SelectTrigger className="h-8 w-28 text-xs" aria-label={`Role for ${m.name || m.email}`}>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="admin">Admin</SelectItem>
                            <SelectItem value="member">Member</SelectItem>
                          </SelectContent>
                        </Select>
                      ) : (
                        <RolePill role={m.role} />
                      )}
                    </Cell>
                    <Cell label="Joined">
                      <Time value={m.joinedAt} />
                    </Cell>
                    <RowActions>
                      {admin && !m.self && (
                        <Button
                          variant="ghost"
                          size="iconSm"
                          className="hover:text-danger"
                          aria-label="Remove member"
                          title="Remove member"
                          onClick={() => {
                            setRemoveErr(undefined)
                            setRemoving(m)
                          }}
                        >
                          <Trash2 />
                        </Button>
                      )}
                    </RowActions>
                  </ListRow>
                ))}
              </ListRows>
            )}
            {org && !org.personal && (
              <div className="border-t border-border px-5 py-3 text-xs text-muted-foreground">
                To leave {org.name}, use <span className="font-medium text-foreground">Leave org</span> in the account menu.
              </div>
            )}
          </ListCard>
        </TabPanel>
      )}

      {tab === 'invites' && admin && (
        <TabPanel tabKey="invites">
          <ListCard>
            <ListBar>
              <span>
                {invites.length.toLocaleString()} {invites.length === 1 ? 'invite' : 'invites'}
                {invites.length > 0 && ` · ${openInvites} open`}
              </span>
            </ListBar>
            {invites.length > 0 && (
              <ListHead cols={INVITE_COLS}>
                <span>Invite</span>
                <span>Created by</span>
                <span>Created</span>
                <span>Expires</span>
                <span>Status</span>
                <span />
              </ListHead>
            )}
            {invites.length === 0 ? (
              <ListEmpty
                icon={MailPlus}
                title="No invites yet"
                hint="An invite is a one-time link. Whoever opens it while signed in joins this org."
                action={
                  <Button variant="secondary" onClick={() => setInviting(true)}>
                    <Plus /> Create invite
                  </Button>
                }
              />
            ) : (
              <ListRows>
                {invites.map((inv) => {
                  const st = inviteStatus(inv, nameOf)
                  return (
                    <ListRow key={inv.id} cols={INVITE_COLS} className={cn(st.key !== 'open' && 'opacity-70')}>
                      <div className="flex min-w-0 items-center gap-3">
                        <IconTile icon={Link2} tone={st.key === 'open' ? 'info' : 'muted'} />
                        <Primary title={`${inv.role === 'admin' ? 'Admin' : 'Member'} invite`} subtitle={inv.id.slice(0, 8)} mono />
                      </div>
                      <Cell label="Created by">
                        <span className="block truncate text-sm text-muted-foreground">{nameOf(inv.createdBy)}</span>
                      </Cell>
                      <Cell label="Created">
                        <Time value={inv.createdAt} />
                      </Cell>
                      <Cell label="Expires">
                        <Time value={inv.expiresAt} />
                      </Cell>
                      <Cell label="Status">
                        <span title={inv.usedAt ? absoluteTime(inv.usedAt) : undefined}>
                          <StatusPill tone={st.tone} className="max-w-full">
                            <span className="truncate">{st.label}</span>
                          </StatusPill>
                        </span>
                      </Cell>
                      <RowActions>
                        {st.key === 'open' && (
                          <Button
                            variant="ghost"
                            size="iconSm"
                            className="hover:text-danger"
                            aria-label="Revoke invite"
                            title="Revoke invite"
                            onClick={() => {
                              setRevokeErr(undefined)
                              setRevoking(inv)
                            }}
                          >
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
        </TabPanel>
      )}

      <RenameDialog
        open={renaming}
        current={org?.name ?? ''}
        onClose={() => setRenaming(false)}
        onSaved={(o) => {
          setRenaming(false)
          setOrg(o)
          toast.success(`Renamed to ${o.name}`)
          void refreshMe()
        }}
      />
      <CreateInviteDialog
        open={inviting}
        orgName={org?.name}
        onClose={() => setInviting(false)}
        onCreated={(inv) => {
          setInviting(false)
          setCreated(inv)
          setTab('invites')
          void loadInvites()
        }}
      />
      <InviteLinkDialog invite={created} orgName={org?.name} onClose={() => setCreated(undefined)} />
      <ConfirmDialog
        open={!!removing}
        title={`Remove ${removing?.name || removing?.email || 'member'}?`}
        description={`They lose access to ${org?.name ?? 'this org'} immediately and land back in their personal org.`}
        confirmLabel="Remove"
        busy={removeBusy}
        error={removeErr}
        onCancel={() => setRemoving(undefined)}
        onConfirm={remove}
      />
      <ConfirmDialog
        open={!!revoking}
        title="Revoke this invite?"
        description="The link stops working immediately. You can create a new one at any time."
        confirmLabel="Revoke"
        busy={revokeBusy}
        error={revokeErr}
        onCancel={() => setRevoking(undefined)}
        onConfirm={revoke}
      />
    </div>
  )
}

function RenameDialog({ open, current, onClose, onSaved }: { open: boolean; current: string; onClose: () => void; onSaved: (o: OrgDTO) => void }) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  useEffect(() => {
    if (open) {
      setName(current)
      setError(undefined)
    }
  }, [open, current])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return setError('Name is required.')
    setBusy(true)
    setError(undefined)
    try {
      onSaved(await api.org.rename(name.trim()))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-w-md">
        <form className="contents" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Rename organisation</DialogTitle>
            <DialogDescription>Everyone in the org sees the new name in their top bar.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <FormField label="Name" required>
              <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus required maxLength={120} />
            </FormField>
            {error && <Callout tone="danger">{error}</Callout>}
          </DialogBody>
          <DialogFooter>
            <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

const ROLE_HINTS: Record<OrgRole, string> = {
  member: 'Sees the servers granted to them, uses the Explorer, and manages their own API keys and audit trail.',
  admin: 'Everything a member can do, plus servers, entitlements, grants, members and invites.',
}

function CreateInviteDialog({
  open,
  orgName,
  onClose,
  onCreated,
}: {
  open: boolean
  orgName?: string
  onClose: () => void
  onCreated: (inv: InviteDTO) => void
}) {
  const [role, setRole] = useState<OrgRole>('member')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  useEffect(() => {
    if (open) {
      setRole('member')
      setError(undefined)
    }
  }, [open])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      onCreated(await api.org.invites.create(role))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-w-md">
        <form className="contents" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Create invite</DialogTitle>
            <DialogDescription>A one-time link to join {orgName ?? 'this org'}. It is shown once, right after creation.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <FormField label="Role" required hint={ROLE_HINTS[role]}>
              <Select value={role} onValueChange={(v) => setRole(v as OrgRole)}>
                <SelectTrigger aria-label="Role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="member">Member</SelectItem>
                  <SelectItem value="admin">Admin</SelectItem>
                </SelectContent>
              </Select>
            </FormField>
            {error && <Callout tone="danger">{error}</Callout>}
          </DialogBody>
          <DialogFooter>
            <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Creating…' : 'Create invite'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function InviteLinkDialog({ invite, orgName, onClose }: { invite?: InviteDTO; orgName?: string; onClose: () => void }) {
  return (
    <Dialog open={!!invite} onOpenChange={(o) => !o && onClose()}>
      <DialogContent onOpenAutoFocus={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>Invite created</DialogTitle>
          <DialogDescription>Copy the link now. It is not stored and cannot be shown again.</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {invite && (
            <>
              <div>
                <p className="text-xs font-semibold text-muted-foreground">
                  {invite.role === 'admin' ? 'Admin' : 'Member'} invite to {orgName ?? 'this org'}
                </p>
                <div className="mt-1.5 flex items-start gap-2 rounded-lg border border-dashed border-border-strong bg-code p-3">
                  <code className="min-w-0 flex-1 break-all font-mono text-xs leading-5 text-code-foreground">{invite.url}</code>
                  {invite.url && <CopyButton value={invite.url} label="Copy invite link" />}
                </div>
              </div>
              <Callout tone="info">
                Works once, for anyone who opens it while signed in. Expires{' '}
                <span className="font-mono text-xs" title={absoluteTime(invite.expiresAt)}>
                  {relativeTime(invite.expiresAt)}
                </span>
                . Revoke it from the Invites tab if it goes astray.
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
