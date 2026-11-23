import { Search } from 'lucide-react'
import { Input } from '../ui/components/input'
import { cn } from '../lib'

/** The search field for a ListBar: sized like the bar's other filters, full-width when stacked. */
export function SearchBox({ value, onChange, placeholder, className }: { value: string; onChange: (value: string) => void; placeholder: string; className?: string }) {
  return (
    <div className={cn('relative w-full sm:w-64', className)}>
      <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
      <Input type="search" value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} aria-label={placeholder} className="h-8 pl-8 text-xs" />
    </div>
  )
}
