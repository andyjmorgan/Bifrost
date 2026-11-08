import { Badge } from '../ui/components/badge'

/** Mono scope tags; beyond `max` the rest collapse into a +N tag with a tooltip. */
export function ScopeBadges({ scopes, max = 3, variant = 'accent' }: { scopes: string[]; max?: number; variant?: 'accent' | 'plum' | 'muted' }) {
  if (!scopes.length) return <span className="text-xs text-muted-foreground">—</span>
  const shown = scopes.slice(0, max)
  const rest = scopes.slice(max)
  return (
    <div className="flex flex-wrap gap-1">
      {shown.map((s) => (
        <Badge key={s} mono variant={variant}>
          {s}
        </Badge>
      ))}
      {rest.length > 0 && (
        <Badge mono title={rest.join(', ')}>
          +{rest.length}
        </Badge>
      )}
    </div>
  )
}
