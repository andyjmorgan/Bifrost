import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { ArrowDown, ArrowUp, Pencil, ShieldCheck, X } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/components/select'
import { FormField } from '../FormField'
import { Callout } from '../Callout'
import { IconTile } from '../IconTile'
import { api, errorMessage, type PolicyDTO, type RouteBindingsDTO } from '../../api'
import { useMe } from '../../me'

type Ref = { id: string; name: string }

/** Display label for a route: the global front door or a server slug. */
export function routeLabel(route: string): string {
  return route === 'mux' ? 'Global (/mcp)' : route
}

/**
 * The ordered set of policies bound to one route. Reused for the global mux and each server.
 * Order matters: policies run top to bottom.
 */
export function BindingsDialog({
  open,
  route,
  current,
  onClose,
  onSaved,
}: {
  open: boolean
  route: string
  current: Ref[]
  onClose: () => void
  onSaved: (b: RouteBindingsDTO) => void
}) {
  const [all, setAll] = useState<PolicyDTO[]>([])
  const [list, setList] = useState<Ref[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  // Reset only when the dialog opens, so a parent re-render mid-edit keeps the working order.
  const currentRef = useRef(current)
  currentRef.current = current
  useEffect(() => {
    if (!open) return
    setList(currentRef.current)
    setError(undefined)
    api.policies
      .list()
      .then(setAll)
      .catch((e) => setError(errorMessage(e)))
  }, [open])

  const available = all.filter((p) => !list.some((l) => l.id === p.id))
  const move = (i: number, d: -1 | 1) =>
    setList((l) => {
      const j = i + d
      if (j < 0 || j >= l.length) return l
      const next = [...l]
      ;[next[i], next[j]] = [next[j], next[i]]
      return next
    })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      onSaved(await api.bindings.put(route, list.map((l) => l.id)))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent onOpenAutoFocus={(e) => e.preventDefault()}>
        <form className="contents" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Policies on {route === 'mux' ? '/mcp' : route}</DialogTitle>
            <DialogDescription>
              Policies run top to bottom.{route === 'mux' ? ' A server’s own policies run before these.' : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            {list.length === 0 ? (
              <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-xs text-muted-foreground">No policies bound.</p>
            ) : (
              <ol className="divide-y divide-border rounded-lg border border-border">
                {list.map((p, i) => (
                  <li key={p.id} className="flex items-center gap-3 px-3 py-2">
                    <span className="w-5 shrink-0 font-mono text-xs text-faint-foreground">{i + 1}</span>
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">{p.name}</span>
                    <Button type="button" variant="ghost" size="iconSm" aria-label="Move up" title="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
                      <ArrowUp />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="iconSm"
                      aria-label="Move down"
                      title="Move down"
                      disabled={i === list.length - 1}
                      onClick={() => move(i, 1)}
                    >
                      <ArrowDown />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="iconSm"
                      className="hover:text-danger"
                      aria-label="Remove"
                      title="Remove"
                      onClick={() => setList((l) => l.filter((x) => x.id !== p.id))}
                    >
                      <X />
                    </Button>
                  </li>
                ))}
              </ol>
            )}
            <FormField label="Add policy" hint={all.length === 0 ? 'No policies yet.' : available.length === 0 ? 'Every policy is bound.' : undefined}>
              <Select
                value=""
                disabled={available.length === 0}
                onValueChange={(id) => {
                  const p = all.find((x) => x.id === id)
                  if (p) setList((l) => [...l, { id: p.id, name: p.name }])
                }}
              >
                <SelectTrigger aria-label="Add policy">
                  <SelectValue placeholder="Choose a policy" />
                </SelectTrigger>
                <SelectContent>
                  {available.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
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
              {busy ? 'Saving…' : 'Save'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** A compact card: the ordered policies bound to a route, with an Edit action. */
export function BindingsCard({ route, title, hint, onChanged }: { route: string; title: string; hint?: string; onChanged?: () => void }) {
  const { isAdmin } = useMe()
  const [bindings, setBindings] = useState<RouteBindingsDTO>()
  const [error, setError] = useState<string>()
  const [editing, setEditing] = useState(false)

  const load = useCallback(
    () =>
      api.bindings
        .get(route)
        .then((b) => {
          setBindings(b)
          setError(undefined)
        })
        .catch((e) => setError(errorMessage(e))),
    [route],
  )
  useEffect(() => {
    void load()
  }, [load])

  const policies = bindings?.policies ?? []

  return (
    <div className="rounded-2xl border border-border bg-card p-5">
      <div className="flex flex-wrap items-center gap-4">
        <IconTile icon={ShieldCheck} tone="success" />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium">{title}</p>
          <div className="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-muted-foreground">
            {!bindings && !error && <span>Loading…</span>}
            {bindings && policies.length === 0 && <span>{hint ?? 'No policies bound.'}</span>}
            {policies.map((p, i) => (
              <span key={p.id} className="inline-flex items-center gap-1.5">
                {i > 0 && <span className="text-faint-foreground">→</span>}
                <span className="font-mono text-faint-foreground">{i + 1}</span>
                <Link to={`/policies/${encodeURIComponent(p.id)}`} className="text-foreground underline decoration-accent/60 underline-offset-[3px] hover:text-accent">
                  {p.name}
                </Link>
              </span>
            ))}
          </div>
        </div>
        {isAdmin && (
          <Button variant="secondary" size="sm" onClick={() => setEditing(true)} disabled={!bindings}>
            <Pencil /> Edit
          </Button>
        )}
      </div>
      {error && (
        <Callout tone="danger" className="mt-4">
          {error}
        </Callout>
      )}
      <BindingsDialog
        open={editing}
        route={route}
        current={policies}
        onClose={() => setEditing(false)}
        onSaved={(b) => {
          setEditing(false)
          setBindings(b)
          toast.success(`Saved policies for ${route === 'mux' ? '/mcp' : route}`)
          onChanged?.()
        }}
      />
    </div>
  )
}
