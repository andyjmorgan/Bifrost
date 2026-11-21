import { UserRound } from 'lucide-react'
import { Checkbox } from '../../ui/components/checkbox'
import { ListBar, ListCard, ListEmpty, ListRows } from '../ListCard'
import type { GrantInput, UserDTO } from '../../api'
import { useMe } from '../../me'
import { absoluteTime, relativeTime } from '../../lib'

/** Who can reach a server through the gateway: everyone who has signed in, each with a checkbox. */
export function GrantsEditor({ grants, onChange, users }: { grants: GrantInput[]; onChange: (grants: GrantInput[]) => void; users: UserDTO[] }) {
  const { userId: self } = useMe()
  const has = (id: string) => grants.some((g) => g.userId === id)
  const toggle = (id: string) => onChange(has(id) ? grants.filter((g) => g.userId !== id) : [...grants, { userId: id }])
  // A grant for someone the user list does not hold (it failed to load, or is stale) stays visible so it can be removed.
  const known = new Set(users.map((u) => u.id))
  const unknown = grants.filter((g) => !known.has(g.userId))

  return (
    <section className="space-y-3">
      <p className="max-w-2xl text-xs text-muted-foreground">
        Who can reach this server through the gateway. Tick a person to let their session and API keys call it.
      </p>
      <ListCard>
        <ListBar>
          <span>
            {grants.length} granted · {users.length} {users.length === 1 ? 'person' : 'people'}
          </span>
        </ListBar>
        {users.length === 0 && unknown.length === 0 ? (
          <ListEmpty icon={UserRound} title="No users yet" hint="People appear here once they have signed in." />
        ) : (
          <ListRows>
            {users.map((u) => {
              const id = `grant-${u.id}`
              return (
                <label key={u.id} htmlFor={id} className="flex cursor-pointer items-center gap-3 px-5 py-3 hover:bg-elevated/45">
                  <Checkbox id={id} checked={has(u.id)} onCheckedChange={() => toggle(u.id)} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">
                      {u.name || u.email || u.id}
                      {u.id === self && <span className="ml-1.5 text-xs font-normal text-muted-foreground">(you)</span>}
                    </span>
                    <span className="block truncate font-mono text-xs text-muted-foreground">{u.email}</span>
                  </span>
                  <span className="shrink-0 font-mono text-[11px] text-muted-foreground" title={absoluteTime(u.lastSeenAt)}>
                    seen {relativeTime(u.lastSeenAt)}
                  </span>
                </label>
              )
            })}
            {unknown.map((g) => (
              <label key={g.userId} className="flex cursor-pointer items-center gap-3 px-5 py-3 hover:bg-elevated/45">
                <Checkbox checked onCheckedChange={() => toggle(g.userId)} aria-label={g.userId} />
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{g.userId}</span>
              </label>
            ))}
          </ListRows>
        )}
      </ListCard>
    </section>
  )
}
