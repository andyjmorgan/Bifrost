import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from '../lib'
import type { Tone } from './StatusPill'

const tones: Record<Tone, string> = {
  success: 'bg-success/10 text-success',
  warning: 'bg-warning/10 text-warning',
  danger: 'bg-danger/10 text-danger',
  info: 'bg-info/10 text-info',
  accent: 'bg-accent/10 text-accent',
  muted: 'bg-elevated text-muted-foreground',
}

/** The leading icon box on a list row. */
export function IconTile({ icon: Icon, tone = 'accent', className, children }: { icon: LucideIcon; tone?: Tone; className?: string; children?: ReactNode }) {
  return (
    <div className={cn('grid size-9 shrink-0 place-items-center rounded-xl', tones[tone], className)}>
      {children ?? <Icon className="size-4" />}
    </div>
  )
}
