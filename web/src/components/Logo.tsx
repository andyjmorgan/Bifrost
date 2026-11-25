import { cn } from '../lib'

/** The Bifrost mark: the light/dark pair follows the theme; the wordmark names the product. */
export function Logo({
  compact = false,
  className,
  imageClassName,
}: {
  compact?: boolean
  className?: string
  imageClassName?: string
}) {
  // The light square (#FAFAF9) sits on a near-identical page tone, so it keeps the guide's subtle shadow.
  const mark = cn('size-8 shrink-0 rounded-[21%] drop-shadow-sm dark:drop-shadow-none', imageClassName)
  return (
    <div className={cn('flex items-center gap-2.5', className)}>
      <img src="/brand/bifrost-light-v2.svg" alt="Bifrost" className={cn(mark, 'dark:hidden')} />
      <img src="/brand/bifrost-dark-v2.svg" alt="Bifrost" className={cn(mark, 'hidden dark:block')} />
      {!compact && (
        <span className="text-lg font-semibold leading-none tracking-tight">Bifrost</span>
      )}
    </div>
  )
}
