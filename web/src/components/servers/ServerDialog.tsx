import { useEffect, useState, type FormEvent } from 'react'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { Textarea } from '../../ui/components/textarea'
import { FormField, FormSection } from '../FormField'
import { Callout } from '../Callout'
import { api, errorMessage, type CreateServerResult, type ServerDTO, type UpdateServerInput } from '../../api'
import { AuthFields, emptyAuthDraft, toAuthInput, validateAuth, type AuthDraft } from './AuthFields'
import { SLUG_MAX, slugBase, slugProblem } from '../rules/selection'

interface Draft {
  name: string
  /** Create only; empty = derived from the name. */
  slug: string
  url: string
  description: string
  auth: AuthDraft
}

const emptyDraft = (): Draft => ({ name: '', slug: '', url: '', description: '', auth: emptyAuthDraft('none') })

function isHttpUrl(s: string): boolean {
  try {
    const u = new URL(s)
    return u.protocol === 'http:' || u.protocol === 'https:'
  } catch {
    return false
  }
}

/**
 * Create or edit a server. In create mode the slug and auth section are part of the form; the
 * result may carry an authorizeUrl, which the caller follows for OAuth consent. In edit mode only
 * name, URL and description change (the slug is fixed; credentials have their own dialog).
 */
export function ServerDialog({
  open,
  server,
  onClose,
  onCreated,
  onUpdated,
}: {
  open: boolean
  server?: ServerDTO
  onClose: () => void
  onCreated?: (result: CreateServerResult) => void
  onUpdated?: (server: ServerDTO) => void
}) {
  const editing = !!server
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    if (!open) return
    setError(undefined)
    setDraft(server ? { name: server.name, slug: server.slug, url: server.url, description: server.description, auth: emptyAuthDraft(server.authKind) } : emptyDraft())
  }, [open, server])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const name = draft.name.trim()
    const url = draft.url.trim()
    if (!name) return setError('Name is required.')
    if (!isHttpUrl(url)) return setError('URL must be an absolute http(s) URL.')
    const slug = draft.slug.trim()
    if (!editing) {
      const slugErr = slug ? slugProblem(slug) : undefined
      if (slugErr) return setError(slugErr)
      const authErr = validateAuth(draft.auth)
      if (authErr) return setError(authErr)
    }
    setBusy(true)
    setError(undefined)
    try {
      if (editing && server) {
        const patch: UpdateServerInput = {}
        if (name !== server.name) patch.name = name
        if (url !== server.url) patch.url = url
        if (draft.description !== server.description) patch.description = draft.description
        const updated = Object.keys(patch).length ? await api.servers.update(server.id, patch) : server
        onUpdated?.(updated)
      } else {
        const result = await api.servers.create({ name, url, description: draft.description, slug: slug || undefined, auth: toAuthInput(draft.auth) })
        onCreated?.(result)
      }
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-w-xl">
        <form className="contents" onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{editing ? 'Edit server' : 'New server'}</DialogTitle>
            <DialogDescription>
              {editing
                ? 'Rename or re-point the server. Its slug and gateway URL never change.'
                : 'Register an upstream MCP server. Agents reach it through the gateway with one of your API keys.'}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <FormSection title="Server">
              <FormField label="Name" required>
                <Input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder="Atlassian" autoFocus required />
              </FormField>
              {!editing && (
                <FormField
                  label="Slug"
                  hint={
                    <>
                      Clients reach it at <span className="font-mono text-foreground">/{draft.slug.trim() || slugBase(draft.name)}/mcp</span> and see its tools as{' '}
                      <span className="font-mono text-foreground">{draft.slug.trim() || slugBase(draft.name)}__tool</span>. Leave empty to derive it from the name. It cannot be
                      changed later.
                    </>
                  }
                >
                  <Input
                    value={draft.slug}
                    maxLength={SLUG_MAX}
                    onChange={(e) => setDraft({ ...draft, slug: e.target.value.toLowerCase() })}
                    placeholder={slugBase(draft.name)}
                    className="font-mono"
                  />
                </FormField>
              )}
              <FormField label="URL" required hint="The upstream Streamable HTTP endpoint.">
                <Input
                  type="url"
                  value={draft.url}
                  onChange={(e) => setDraft({ ...draft, url: e.target.value })}
                  placeholder="https://mcp.example.com/mcp"
                  className="font-mono"
                  required
                />
              </FormField>
              <FormField label="Description">
                <Textarea
                  value={draft.description}
                  onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                  placeholder="What this server is for"
                  rows={2}
                />
              </FormField>
            </FormSection>
            {!editing && (
              <FormSection title="Authentication">
                <AuthFields value={draft.auth} onChange={(auth) => setDraft({ ...draft, auth })} disabled={busy} />
              </FormSection>
            )}
            {error && <Callout tone="danger">{error}</Callout>}
          </DialogBody>
          <DialogFooter>
            <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Saving…' : editing ? 'Save changes' : draft.auth.kind === 'oauth' ? 'Create and authorize' : 'Create server'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
