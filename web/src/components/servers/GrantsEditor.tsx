import { Check, UserRound } from 'lucide-react'
import { Checkbox } from '../../ui/components/checkbox'
import { Badge } from '../../ui/components/badge'
import { ListBar, ListCard, ListEmpty, ListRows } from '../ListCard'
import type { GrantInput, MemberDTO, OrgRole } from '../../api'
import { cn } from '../../lib'

const ROLES: { id: OrgRole; label: string }[] = [
  { id: 'admin', label: 'Admins' },
  { id: 'member', label: 'Members' },
]

/** Who holds a server: the two roles as toggles, then every member with a checkbox. */
export function GrantsEditor({ grants, onChange, members }: { grants: GrantInput[]; onChange: (grants: GrantInput[]) => void; members: MemberDTO[] }) {
  const has = (t: GrantInput['subjectType'], id: string) => grants.some((g) => g.subjectType === t && g.subjectId === id)
  const toggle = (t: GrantInput['subjectType'], id: string) =>
    onChange(has(t, id) ? grants.filter((g) => !(g.subjectType === t && g.subjectId === id)) : [...grants, { subjectType: t, subjectId: id }])
  const known = new Set(members.map((m) => m.userId))
  const orphans = grants.filter((g) => g.subjectType === 'user' && !known.has(g.subjectId))
  const userCount = grants.filter((g) => g.subjectType === 'user').length

  return (
    <section className="space-y-3">
      <p className="max-w-2xl text-xs text-muted-foreground">
        Who can reach this server: everyone with a ticked role, plus the people ticked below.
      </p>
      <ListCard>
        <ListBar>
          <span className="flex flex-wrap items-center gap-2">
            <span className="mr-1 text-[10px] font-semibold uppercase tracking-[0.16em]">Roles</span>
            {ROLES.map((r) => {
              const on = has('role', r.id)
              return (
                <button
                  key={r.id}
                  type="button"
                  aria-pressed={on}
                  onClick={() => toggle('role', r.id)}
                  className={cn(
                    'inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-lg border px-3 text-xs font-medium transition-colors',
                    on ? 'border-accent/20 bg-accent/10 text-accent' : 'bg-transparent text-muted-foreground hover:bg-elevated hover:text-foreground',
                  )}
                >
                  {on && <Check className="size-3.5" />}
                  {r.label}
                </button>
              )
            })}
          </span>
          <span>
            {userCount} {userCount === 1 ? 'person' : 'people'}
          </span>
        </ListBar>
        {members.length === 0 && orphans.length === 0 ? (
          <ListEmpty icon={UserRound} title="No members" hint="Invite people from Members to grant them this server directly." />
        ) : (
          <ListRows>
            {members.map((m) => {
              const id = `grant-${m.userId}`
              return (
                <label key={m.userId} htmlFor={id} className="flex cursor-pointer items-center gap-3 px-5 py-3 hover:bg-elevated/45">
                  <Checkbox id={id} checked={has('user', m.userId)} onCheckedChange={() => toggle('user', m.userId)} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">
                      {m.name || m.email}
                      {m.self && <span className="ml-1.5 text-xs font-normal text-muted-foreground">(you)</span>}
                    </span>
                    <span className="block truncate font-mono text-xs text-muted-foreground">{m.email}</span>
                  </span>
                  <Badge variant={m.role === 'admin' ? 'accent' : 'muted'}>{m.role === 'admin' ? 'Admin' : 'Member'}</Badge>
                </label>
              )
            })}
            {orphans.map((g) => (
              <label key={g.subjectId} className="flex cursor-pointer items-center gap-3 px-5 py-3 hover:bg-elevated/45">
                <Checkbox checked onCheckedChange={() => toggle('user', g.subjectId)} aria-label={g.subjectId} />
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{g.subjectId}</span>
                <span className="shrink-0 text-[11px] text-warning">not in this org</span>
              </label>
            ))}
          </ListRows>
        )}
      </ListCard>
    </section>
  )
}
