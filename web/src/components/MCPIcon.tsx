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

/** Theme-specific icons take priority; otherwise preserve the server's order. */
export function MCPIcon({ icons, ...tile }: TileProps & { icons?: unknown }) {
  const theme = useTheme()
  const valid = Array.isArray(icons) ? icons.filter(isIcon) : []
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
