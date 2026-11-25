import { useState, type ComponentProps } from 'react'
import type { MCPIcon as Icon } from '../api'
import { useTheme } from '../theme'
import { IconTile } from './IconTile'

type TileProps = Omit<ComponentProps<typeof IconTile>, 'children'>

/** Capability definitions are untyped JSON, just like upstream icon metadata. */
export function definitionIcons(definition: unknown): unknown {
  return definition && typeof definition === 'object' && 'icons' in definition ? definition.icons : undefined
}

function isIcon(value: unknown): value is Icon {
  if (!value || typeof value !== 'object' || !('src' in value) || typeof value.src !== 'string') return false
  // Render remote images and embedded image data only. SVGs stay inside img,
  // never injected into the document. Reject relative and executable URLs.
  return /^https?:\/\//i.test(value.src) || /^data:image\/(png|jpeg|gif|webp|avif|svg\+xml|x-icon|vnd\.microsoft\.icon)[;,]/i.test(value.src)
}

/** Prefer a vector or the smallest advertised bitmap that covers 24px at common HiDPI scale. */
function sizeRank(icon: Icon): number {
  const sizes = Array.isArray(icon.sizes) ? icon.sizes.filter((s): s is string => typeof s === 'string') : []
  if (sizes.includes('any')) return 0
  const dimensions = sizes.map((s) => /^(\d+)x(\d+)$/i.exec(s)).filter((m) => m !== null).map((m) => Math.min(Number(m[1]), Number(m[2])))
  const suitable = dimensions.filter((size) => size >= 48)
  if (suitable.length) return Math.min(...suitable)
  // Unknown dimensions remain a usable alternative, ahead of known undersized images.
  return dimensions.length ? 10000 - Math.max(...dimensions) : 1000
}

/** Theme-specific icons take priority, then advertised resolution. */
export function MCPIcon({ icons, ...tile }: TileProps & { icons?: unknown }) {
  const theme = useTheme()
  const valid = Array.isArray(icons) ? icons.filter(isIcon).sort((a, b) => sizeRank(a) - sizeRank(b)) : []
  const sources = [...new Set([
    ...valid.filter((i) => i.theme === theme),
    ...valid.filter((i) => !i.theme),
    ...valid.filter((i) => i.theme && i.theme !== theme),
  ].map((i) => i.src))]
  // A new icon set or theme gets a fresh attempt, including after a previous
  // image failed. Failed images advance to the next advertised alternative.
  return <IconImage key={JSON.stringify(sources)} sources={sources} {...tile} />
}

function IconImage({ sources, ...tile }: TileProps & { sources: string[] }) {
  const [index, setIndex] = useState(0)
  const src = sources[index]
  return (
    <IconTile {...tile}>
      {src ? (
        <img
          key={src}
          src={src}
          alt=""
          aria-hidden="true"
          className="size-6 object-contain"
          loading="lazy"
          decoding="async"
          referrerPolicy="no-referrer"
          onError={() => setIndex((i) => i + 1)}
        />
      ) : undefined}
    </IconTile>
  )
}
