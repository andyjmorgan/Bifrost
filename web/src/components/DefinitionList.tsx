import type { ReactNode } from 'react'
import { cn } from '../lib'

/** The read-only "view" card: label/value rows separated by hairlines, never disabled inputs. */
export function DefinitionList({ className, children }: { className?: string; children: ReactNode }) {
  return <dl className={cn('divide-y divide-border rounded-2xl border border-border bg-card', className)}>{children}</dl>
}

export function Definition({ label, mono = false, className, children }: { label: string; mono?: boolean; className?: string; children: ReactNode }) {
  return (
    <div className={cn('grid gap-1 px-5 py-3.5 sm:grid-cols-[180px_minmax(0,1fr)] sm:gap-4', className)}>
      <dt className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground sm:pt-1">{label}</dt>
      <dd className={cn('min-w-0 break-words text-sm', mono && 'font-mono text-xs text-muted-foreground')}>{children}</dd>
    </div>
  )
}
