import * as React from 'react'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { cn } from '../../lib'
import { Button } from './button'

// A right-anchored, full-height panel on the Dialog primitive for detail views that should keep
// the page visible behind them. Two bands: a bordered header (title, meta line, close) and a
// scrolling body. The scrim is lighter than the centred dialog's; Esc, the close button and a
// click outside all dismiss. Full width below `sm`, a fixed column from there.

const Sheet = DialogPrimitive.Root
const SheetTrigger = DialogPrimitive.Trigger
const SheetClose = DialogPrimitive.Close

const SheetContent = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content>
>(({ className, children, ...props }, ref) => (
  <DialogPrimitive.Portal>
    <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/40 animate-overlay-in" />
    <DialogPrimitive.Content
      ref={ref}
      className={cn(
        'fixed inset-y-0 right-0 z-50 flex h-dvh w-full flex-col border-l border-border bg-card text-foreground shadow-2xl outline-none animate-sheet-in sm:w-[min(640px,calc(100%-3rem))] xl:w-[680px]',
        className,
      )}
      {...props}
    >
      {children}
    </DialogPrimitive.Content>
  </DialogPrimitive.Portal>
))
SheetContent.displayName = 'SheetContent'

/** Title band: title + meta on the left, close on the right. */
function SheetHeader({ className, children, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn('flex items-start justify-between gap-4 border-b border-border px-5 py-4 sm:px-6', className)} {...props}>
      <div className="min-w-0 flex-1">{children}</div>
      <DialogPrimitive.Close asChild>
        <Button variant="ghost" size="icon" className="-mr-2 -mt-1 shrink-0" aria-label="Close">
          <X className="size-5" />
        </Button>
      </DialogPrimitive.Close>
    </div>
  )
}

/** The scrolling band. */
function SheetBody({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-5 sm:px-6', className)} {...props} />
}

const SheetTitle = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Title>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Title ref={ref} className={cn('truncate text-lg font-semibold leading-7 tracking-tight', className)} {...props} />
))
SheetTitle.displayName = 'SheetTitle'

const SheetDescription = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Description>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Description ref={ref} className={cn('mt-1 text-xs leading-5 text-muted-foreground', className)} {...props} />
))
SheetDescription.displayName = 'SheetDescription'

export { Sheet, SheetTrigger, SheetClose, SheetContent, SheetHeader, SheetBody, SheetTitle, SheetDescription }
