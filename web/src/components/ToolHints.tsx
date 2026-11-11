import { StatusPill } from './StatusPill'
import type { MCPToolAnnotations } from '../api'

// ToolHints renders a tool's behaviour annotations as pills, with the spec
// defaults applied (readOnly false, destructive true, idempotent false,
// openWorld true). A tool with no annotations says so rather than looking
// destructive by default.
export function ToolHints({ annotations, compact }: { annotations?: MCPToolAnnotations; compact?: boolean }) {
  if (!annotations) return <StatusPill tone="muted">no hints</StatusPill>
  const readOnly = annotations.readOnlyHint === true
  const destructive = !readOnly && annotations.destructiveHint !== false
  const idempotent = annotations.idempotentHint === true
  const openWorld = annotations.openWorldHint !== false
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      {readOnly ? <StatusPill tone="success">read-only</StatusPill> : destructive ? <StatusPill tone="danger">destructive</StatusPill> : <StatusPill tone="warning">writes</StatusPill>}
      {!compact && idempotent && <StatusPill tone="muted">idempotent</StatusPill>}
      {!compact && <StatusPill tone={openWorld ? 'info' : 'muted'}>{openWorld ? 'open world' : 'closed'}</StatusPill>}
      {compact && (idempotent || !openWorld) && (
        <StatusPill tone="muted">{[idempotent && 'idempotent', !openWorld && 'closed'].filter(Boolean).join(' · ')}</StatusPill>
      )}
    </span>
  )
}
