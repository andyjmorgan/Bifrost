import { cn } from '../lib'

/** Pretty-print when the value is JSON (string or object); otherwise show the raw text. */
export function prettyJson(value: unknown): string {
  if (typeof value === 'string') {
    try {
      return JSON.stringify(JSON.parse(value), null, 2)
    } catch {
      return value
    }
  }
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

export function CodeBlock({ children, className, wrap = false }: { children: string; className?: string; wrap?: boolean }) {
  return (
    <pre
      className={cn(
        'max-h-[480px] overflow-auto rounded-lg bg-code p-3 font-mono text-xs leading-5 text-code-foreground',
        wrap && 'whitespace-pre-wrap break-words',
        className,
      )}
    >
      {children}
    </pre>
  )
}

export function JsonBlock({ value, className }: { value: unknown; className?: string }) {
  return <CodeBlock className={className}>{prettyJson(value)}</CodeBlock>
}
