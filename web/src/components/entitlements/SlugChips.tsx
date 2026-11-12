import type { EntitlementRefDTO } from '../../api'
import { Badge } from '../../ui/components/badge'

/** An entitlement's slugs as mono tags; past `max` the rest fold into a +N tag with a tooltip. */
export function SlugChips({ entitlements, max = 3 }: { entitlements: EntitlementRefDTO[]; max?: number }) {
  if (!entitlements.length) return <span className="text-xs text-muted-foreground">None</span>
  const shown = entitlements.slice(0, max)
  const rest = entitlements.slice(max)
  return (
    <div className="flex flex-wrap items-center gap-1">
      {shown.map((e) => (
        <Badge key={e.id} mono title={`${e.name} · ${e.grants} ${e.grants === 1 ? 'grant' : 'grants'}`}>
          {e.slug}
        </Badge>
      ))}
      {rest.length > 0 && (
        <Badge mono variant="outline" title={rest.map((e) => e.slug).join(', ')}>
          +{rest.length}
        </Badge>
      )}
    </div>
  )
}
