import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { toast } from 'sonner'
import { Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { Button } from '../ui/components/button'
import { Badge } from '../ui/components/badge'
import { PageHeader } from '../components/PageHeader'
import { Cell, ListBar, ListCard, ListEmpty, ListHead, ListRow, ListRows, Primary, RowActions } from '../components/ListCard'
import { IconTile } from '../components/IconTile'
import { Callout } from '../components/Callout'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { BindingsCard } from '../components/policies/Bindings'
import { api, errorMessage, type PolicyBindingRef, type PolicyDTO } from '../api'
import { absoluteTime, relativeTime } from '../lib'
import { useMe } from '../me'

const COLS = 'minmax(0,1.6fr) 90px minmax(0,1.2fr) 130px 56px'

export function PoliciesPage() {
  const navigate = useNavigate()
  const { isAdmin } = useMe()
  const [policies, setPolicies] = useState<PolicyDTO[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const [deleting, setDeleting] = useState<PolicyDTO>()
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [deleteErr, setDeleteErr] = useState<string>()

  const load = useCallback(
    () =>
      api.policies
        .list()
        .then((items) => {
          setPolicies(items)
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e)))
        .finally(() => setLoaded(true)),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  const remove = async () => {
    if (!deleting) return
    setDeleteBusy(true)
    setDeleteErr(undefined)
    try {
      await api.policies.remove(deleting.id)
      toast.success(`Deleted ${deleting.name}`)
      setDeleting(undefined)
      void load()
    } catch (e) {
      setDeleteErr(errorMessage(e))
    } finally {
      setDeleteBusy(false)
    }
  }

  const newPolicy = () => navigate('/policies/new')

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Gateway"
        title="Policies"
        description="Ordered CEL rules that hide, rename or gate tools. Bind them to the global route or to a server."
        actions={
          isAdmin ? (
            <Button variant="primary" onClick={newPolicy}>
              <Plus /> New policy
            </Button>
          ) : undefined
        }
      />

      {error && <Callout tone="danger">{error}</Callout>}

      <BindingsCard route="mux" title="Global route (/mcp)" hint="No policies bound. Server policies still apply." onChanged={load} />

      <ListCard>
        <ListBar>
          <span>
            {policies.length.toLocaleString()} {policies.length === 1 ? 'policy' : 'policies'}
          </span>
        </ListBar>
        {policies.length > 0 && (
          <ListHead cols={COLS}>
            <span>Policy</span>
            <span>Rules</span>
            <span>Bound to</span>
            <span>Updated</span>
            <span />
          </ListHead>
        )}
        {policies.length === 0 ? (
          <ListEmpty
            icon={ShieldCheck}
            title={loaded ? 'No policies yet' : 'Loading…'}
            hint={loaded ? 'Create one to hide destructive tools or ask for confirmation before a call.' : undefined}
            action={
              loaded && isAdmin ? (
                <Button variant="secondary" onClick={newPolicy}>
                  <Plus /> New policy
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ListRows>
            {policies.map((p) => (
              <ListRow key={p.id} cols={COLS} onClick={() => navigate(`/policies/${encodeURIComponent(p.id)}`)}>
                <div className="flex min-w-0 items-center gap-3">
                  <IconTile icon={ShieldCheck} tone="success" />
                  <Primary
                    title={
                      <span className="inline-flex max-w-full items-center gap-2">
                        <span className="truncate">{p.name}</span>
                        {p.invert && <Badge variant="info">hidden by default</Badge>}
                      </span>
                    }
                    subtitle={p.description || 'No description.'}
                  />
                </div>
                <Cell label="Rules">
                  <span className="font-mono text-xs text-muted-foreground">{p.rules.length}</span>
                </Cell>
                <Cell label="Bound to">
                  <BindingPills bindings={p.bindings ?? []} />
                </Cell>
                <Cell label="Updated">
                  <span className="block truncate font-mono text-xs text-muted-foreground" title={absoluteTime(p.updatedAt)}>
                    {relativeTime(p.updatedAt)}
                  </span>
                </Cell>
                <RowActions>
                  {isAdmin && (
                    <Button variant="ghost" size="iconSm" className="hover:text-danger" aria-label="Delete" title="Delete" onClick={() => setDeleting(p)}>
                      <Trash2 />
                    </Button>
                  )}
                </RowActions>
              </ListRow>
            ))}
          </ListRows>
        )}
      </ListCard>

      <ConfirmDialog
        open={!!deleting}
        title={`Delete ${deleting?.name ?? 'policy'}?`}
        description="It is removed from every route it is bound to. This cannot be undone."
        busy={deleteBusy}
        error={deleteErr}
        onCancel={() => {
          setDeleting(undefined)
          setDeleteErr(undefined)
        }}
        onConfirm={remove}
      />
    </div>
  )
}

/** Where a policy is attached, as small mono tags; three or more overflow into +N. */
function BindingPills({ bindings }: { bindings: PolicyBindingRef[] }) {
  if (!bindings.length) return <span className="text-xs text-faint-foreground">Not bound</span>
  const shown = bindings.slice(0, 3)
  const rest = bindings.slice(3)
  return (
    <div className="flex flex-wrap gap-1">
      {shown.map((b) => (
        <Badge key={b.route} mono variant={b.route === 'mux' ? 'accent' : 'muted'} title={b.route === 'mux' ? 'Global (/mcp)' : b.serverName}>
          {b.route}
        </Badge>
      ))}
      {rest.length > 0 && (
        <Badge mono title={rest.map((b) => b.route).join(', ')}>
          +{rest.length}
        </Badge>
      )}
    </div>
  )
}
