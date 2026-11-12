import { useEffect, useState, type FormEvent } from 'react'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Textarea } from '../../ui/components/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/components/select'
import { FormField } from '../FormField'
import { Callout } from '../Callout'
import { api, errorMessage, type EntitlementDTO, type ServerDTO } from '../../api'
import { SLUG_MAX, slugBase, slugProblem } from './selection'

/**
 * Name, slug and description for a new entitlement on one server. Given `server` it is fixed (the
 * server detail page); given `servers` instead, a server select comes first (the Entitlements page). The slug follows the name (the
 * same rule the server uses) until the user edits it. It is created offering everything with no
 * grants; the caller then opens the editor to narrow and grant it.
 */
export function NewEntitlementDialog({
  open,
  server: fixed,
  servers = [],
  onClose,
  onCreated,
}: {
  open: boolean
  server?: ServerDTO
  servers?: ServerDTO[]
  onClose: () => void
  onCreated: (e: EntitlementDTO) => void
}) {
  const [serverId, setServerId] = useState('')
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugTouched, setSlugTouched] = useState(false)
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    if (!open) return
    setServerId('')
    setName('')
    setSlug('')
    setSlugTouched(false)
    setDescription('')
    setError(undefined)
  }, [open])

  const server = fixed ?? servers.find((s) => s.id === serverId)

  const onName = (v: string) => {
    setName(v)
    if (!slugTouched) setSlug(v.trim() ? slugBase(v) : '')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!server) return setError('Choose a server.')
    const n = name.trim()
    if (!n) return setError('Name is required.')
    const problem = slugProblem(slug)
    if (problem) return setError(problem)
    setBusy(true)
    setError(undefined)
    try {
      onCreated(await api.entitlements.create(server.id, { name: n, slug, description: description.trim(), selection: {} }))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-w-lg">
        <form className="contents" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>New entitlement</DialogTitle>
            <DialogDescription>
              A named view of {server?.name ?? 'a server'} under its own slug. It starts offering everything with no grants; narrow it and grant it next.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            {!fixed && (
              <FormField label="Server" required>
                <Select value={serverId} onValueChange={setServerId}>
                  <SelectTrigger aria-label="Server" autoFocus>
                    <SelectValue placeholder="Choose a server" />
                  </SelectTrigger>
                  <SelectContent>
                    {servers.map((s) => (
                      <SelectItem key={s.id} value={s.id}>
                        {s.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FormField>
            )}
            <FormField label="Name" required>
              <Input value={name} onChange={(e) => onName(e.target.value)} placeholder="Read only" autoFocus={!!fixed} required />
            </FormField>
            <FormField
              label="Slug"
              required
              hint={
                <>
                  Clients see tools as <span className="font-mono text-foreground">{slug || 'slug'}__tool</span> and reach it at{' '}
                  <span className="font-mono text-foreground">/{slug || 'slug'}/mcp</span>. It cannot be changed later.
                </>
              }
            >
              <Input
                value={slug}
                maxLength={SLUG_MAX}
                onChange={(e) => {
                  setSlugTouched(true)
                  setSlug(e.target.value.toLowerCase())
                }}
                placeholder="coin-ro"
                className="font-mono"
                required
              />
            </FormField>
            <FormField label="Description">
              <Textarea value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Who this is for" rows={2} />
            </FormField>
            {error && <Callout tone="danger">{error}</Callout>}
          </DialogBody>
          <DialogFooter>
            <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Creating…' : 'Create and edit'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
