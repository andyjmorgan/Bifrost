import { useState } from 'react'
import { FileText, Info, MessageSquareText, Sparkles, Variable, Wrench } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { MCPIcon, definitionIcons } from '../MCPIcon'
import { Primary } from '../ListCard'
import { CapabilitySheet, type InspectableCapability } from './CapabilitySheet'

const icons = { tool: Wrench, prompt: MessageSquareText, resource: FileText, template: Variable, skill: Sparkles }
const object = (v: unknown): Record<string, unknown> => v && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : {}
const string = (v: unknown) => typeof v === 'string' ? v : ''

export function capabilityText(cap: InspectableCapability) {
  const def = object(cap.definition)
  const frontmatter = object(def.frontmatter)
  return {
    title: string(def.title) || string(object(def.annotations).title) || string(frontmatter.name),
    description: string(def.description) || string(frontmatter.description),
    uri: string(def.uri) || string(def.uriTemplate),
    name: string(def.name),
  }
}

export function matchesCapability(cap: InspectableCapability, query: string) {
  const q = query.trim().toLowerCase()
  return !q || [cap.name, ...Object.values(capabilityText(cap))].some((v) => v.toLowerCase().includes(q))
}

export function CapabilitySummary({ cap }: { cap: InspectableCapability }) {
  const text = capabilityText(cap)
  return (
    <div className="flex min-w-0 flex-1 items-center gap-3">
      <MCPIcon icons={definitionIcons(cap.definition)} icon={icons[cap.kind]} tone="muted" />
      <Primary title={<span className="font-mono text-xs" title={cap.name}>{cap.name}</span>} subtitle={text.description || text.title || text.uri} />
    </div>
  )
}

/** Separate from selection labels and row actions: inspection never changes an approval or invokes a capability. */
export function CapabilityInfo({ cap }: { cap: InspectableCapability }) {
  const [open, setOpen] = useState(false)
  return (
    <span className="shrink-0" onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
      <Button variant="ghost" size="iconSm" aria-label={`Inspect ${cap.name}`} title="View full definition" onClick={() => setOpen(true)}><Info /></Button>
      <CapabilitySheet cap={open ? cap : undefined} onClose={() => setOpen(false)} />
    </span>
  )
}
