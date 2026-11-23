import { Badge } from '../ui/components/badge'

/**
 * Mono scope tags; beyond `max` the rest collapse into a +N tag with a tooltip.
 *
 * `label` shortens what a tag shows (OAuth scopes can be whole URLs); the tag then truncates and
 * carries the full scope as its tooltip.
 */
export function ScopeBadges({
  scopes,
  max = 3,
  variant = 'accent',
  label,
}: {
  scopes: string[]
  max?: number
  variant?: 'accent' | 'plum' | 'muted'
  label?: (scope: string) => string
}) {
  if (!scopes.length) return <span className="text-xs text-muted-foreground">—</span>
  const shown = scopes.slice(0, max)
  const rest = scopes.slice(max)
  return (
    <div className="flex min-w-0 flex-wrap gap-1">
      {shown.map((s) =>
        label ? (
          <Badge key={s} mono variant={variant} title={s} className="max-w-[min(11rem,100%)]">
            <span className="truncate">{label(s)}</span>
          </Badge>
        ) : (
          <Badge key={s} mono variant={variant}>
            {s}
          </Badge>
        ),
      )}
      {rest.length > 0 && (
        <Badge mono title={rest.join(', ')}>
          +{rest.length}
        </Badge>
      )}
    </div>
  )
}
