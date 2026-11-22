import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { BookOpen, FileText, ListTree, Sparkles } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Input } from '../../ui/components/input'
import { ListBar, ListCard, ListEmpty, ListRow, ListRows, Primary } from '../../components/ListCard'
import { IconTile } from '../../components/IconTile'
import { Callout } from '../../components/Callout'
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '../../ui/components/sheet'
import { api, errorMessage, type E2EResult, type ListSkillsResult, type MCPSkill, type MCPSkillFile, type ReadResourceResult } from '../../api'
import type { TabProps } from '../Explorer'
import { Definition, ListOutcome, MUX, StreamPanel, collect, formatBytes, type StreamEvent } from './shared'
import { ReadResult } from './ResourcesTab'

/**
 * A skill's files, when the server lists them. A server may send something else instead, such as
 * "dynamic" for a skill whose content is generated on each read; then only SKILL.md is known.
 */
const filesOf = (s: MCPSkill): MCPSkillFile[] | undefined => (Array.isArray(s.resources) ? s.resources.filter((f) => f && typeof f.uri === 'string') : undefined)

const skillName = (s: MCPSkill) => s.frontmatter?.name ?? s.uri.split('/').slice(-2, -1)[0] ?? s.uri

/**
 * The skills extension on a direct route: skills/list as the client gets it, after the server's
 * skills/list.post rules, and each skill's files read with resources/read.
 */
export function SkillsTab({ route, keyId, fields }: TabProps) {
  const [listing, setListing] = useState(false)
  const [listResult, setListResult] = useState<E2EResult<ListSkillsResult>>()
  const [filter, setFilter] = useState('')
  const [selectedUri, setSelectedUri] = useState<string>()
  const [fileUri, setFileUri] = useState<string>()
  const [reading, setReading] = useState(false)
  const [readResult, setReadResult] = useState<E2EResult<ReadResourceResult>>()
  const [readEvents, setReadEvents] = useState<StreamEvent[]>([])

  const skills = listResult?.result?.skills ?? []
  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q ? skills.filter((s) => [skillName(s), s.frontmatter?.description, s.uri].some((v) => v?.toLowerCase().includes(q))) : skills
  }, [skills, filter])
  const selected = skills.find((s) => s.uri === selectedUri)

  const list = async () => {
    setListing(true)
    setListResult(undefined)
    setSelectedUri(undefined)
    try {
      const r = await api.e2e.listSkills(route, { apiKeyId: keyId || undefined })
      setListResult(r)
      if (!r.ok) toast.error('skills/list failed', { description: r.error })
    } catch (e) {
      setListResult({ ok: false, latencyMs: 0, endpoint: '', error: errorMessage(e) })
      toast.error('skills/list failed', { description: errorMessage(e) })
    } finally {
      setListing(false)
    }
  }

  const read = async (uri: string) => {
    setFileUri(uri)
    setReading(true)
    setReadResult(undefined)
    setReadEvents([])
    try {
      setReadResult(await api.e2e.readResource(route, { apiKeyId: keyId || undefined, uri }, collect(setReadEvents)))
    } catch (e) {
      setReadResult({ ok: false, latencyMs: 0, endpoint: '', error: errorMessage(e) })
    } finally {
      setReading(false)
    }
  }

  const open = (s: MCPSkill) => {
    setSelectedUri(s.uri)
    void read(s.uri)
  }

  return (
    <div className="space-y-6">
      <div className="rounded-2xl border border-border bg-card p-5">
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end">
          {fields(route === MUX ? 'Skills are served on a direct route only: pick a server.' : 'skills/list, after the server’s skills/list.post rules.')}
          <Button variant="primary" onClick={list} disabled={listing || route === MUX} className="md:mb-[26px]">
            <ListTree /> {listing ? 'Listing…' : 'List skills'}
          </Button>
        </div>
        {route === MUX && (
          <Callout tone="info" className="mt-4">
            The mux route does not serve the skills extension yet. Pick a server&rsquo;s direct route to list its skills.
          </Callout>
        )}
        {listResult && <ListOutcome result={listResult} method="skills/list" />}
      </div>

      <ListCard>
        <ListBar>
          <span>
            {skills.length.toLocaleString()} {skills.length === 1 ? 'skill' : 'skills'}
          </span>
          {skills.length > 0 && (
            <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter" aria-label="Filter skills" className="h-8 w-48 text-xs" />
          )}
        </ListBar>
        {visible.length === 0 ? (
          <ListEmpty
            icon={Sparkles}
            title={listResult ? (skills.length ? 'No skills match' : 'No skills returned') : 'No skills listed yet'}
            hint={listResult ? undefined : 'Pick a server that offers skills, then list them.'}
          />
        ) : (
          <ListRows>
            {visible.map((s) => (
              <ListRow key={s.uri} cols="minmax(0,1fr) auto" onClick={() => open(s)} className="p-4">
                <div className="flex min-w-0 items-center gap-3">
                  <IconTile icon={Sparkles} tone="muted" />
                  <Primary title={<span className="font-mono text-xs">{skillName(s)}</span>} subtitle={s.frontmatter?.description?.split('\n')[0]} />
                </div>
                <span className="font-mono text-xs text-muted-foreground">{fileCount(s)}</span>
              </ListRow>
            ))}
          </ListRows>
        )}
      </ListCard>

      <Sheet open={!!selected} onOpenChange={(o) => !o && setSelectedUri(undefined)}>
        <SheetContent onOpenAutoFocus={(ev) => ev.preventDefault()}>
          {selected && (
            <>
              <SheetHeader>
                <SheetTitle className="break-all font-mono text-base">{skillName(selected)}</SheetTitle>
                <SheetDescription asChild>
                  <div className="mt-1 space-y-1">
                    {selected.frontmatter?.description && <p className="whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{selected.frontmatter.description}</p>}
                    <p className="break-all font-mono text-xs text-muted-foreground">{selected.uri}</p>
                  </div>
                </SheetDescription>
              </SheetHeader>
              <SheetBody>
                <div className="space-y-5">
                  <Definition value={selected} />
                  <div>
                    <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">Files</p>
                    <ul className="divide-y divide-border rounded-lg border border-border">
                      {(filesOf(selected) ?? [{ uri: selected.uri }]).map((f) => (
                        <li key={f.uri}>
                          <button
                            type="button"
                            onClick={() => read(f.uri)}
                            className="flex w-full items-center gap-3 px-3 py-2 text-left text-xs hover:bg-elevated/60 aria-[current=true]:bg-elevated"
                            aria-current={f.uri === fileUri}
                          >
                            <FileText className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                            <span className="min-w-0 flex-1 break-all font-mono">{f.uri.replace(/^.*\//, '')}</span>
                            {f.size !== undefined && <span className="font-mono text-muted-foreground">{formatBytes(f.size)}</span>}
                            <BookOpen className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                          </button>
                        </li>
                      ))}
                    </ul>
                    {!filesOf(selected) && selected.resources !== undefined && (
                      <p className="mt-1.5 text-xs leading-5 text-faint-foreground">
                        The server lists this skill&rsquo;s files as <span className="font-mono">{String(selected.resources)}</span>, so only SKILL.md is shown.
                      </p>
                    )}
                  </div>
                  {(reading || readEvents.length > 0) && <StreamPanel events={readEvents} busy={reading} />}
                  {readResult && <ReadResult result={readResult} />}
                  {!readResult && !reading && <Button variant="ghost" onClick={() => read(selected.uri)}>Read SKILL.md</Button>}
                </div>
              </SheetBody>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function fileCount(s: MCPSkill): string {
  const files = filesOf(s)
  if (!files) return typeof s.resources === 'string' ? s.resources : '1 file'
  return `${files.length.toLocaleString()} ${files.length === 1 ? 'file' : 'files'}`
}
