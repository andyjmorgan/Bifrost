import { useState } from 'react'
import { cn } from '../../lib'

/**
 * A provider's logo in a tile the size of an IconTile, falling back to its initial when there is
 * no icon or it fails to load. Brought over from the Vault console: the gateway had no tile for a
 * remote logo. The white backing is deliberate, so brand marks drawn for a light page stay legible
 * in the dark theme.
 */
export function ProviderIcon({ src, name, className }: { src?: string | null; name: string; className?: string }) {
  const [failed, setFailed] = useState(false)
  if (!src || failed) {
    return (
      <div className={cn('grid size-9 shrink-0 place-items-center rounded-xl bg-accent/10 text-sm font-semibold text-accent', className)}>
        {(name || '?').charAt(0).toUpperCase()}
      </div>
    )
  }
  return (
    <div className={cn('grid size-9 shrink-0 place-items-center overflow-hidden rounded-xl border border-border bg-white', className)}>
      <img src={src} alt="" className="size-[62%] object-contain" onError={() => setFailed(true)} />
    </div>
  )
}
