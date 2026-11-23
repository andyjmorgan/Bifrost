import { useState } from 'react'
import { Eye } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { Callout } from '../Callout'
import { CopyButton } from '../CopyButton'
import { errorMessage } from '../../api'

// Middle-truncate what is displayed so a very long value (an OAuth access token can run to
// thousands of characters) cannot push the dialog off-screen. Copy still copies the whole value.
const middleTruncate = (s: string, max = 96) => (s.length > max ? `${s.slice(0, max / 2 - 1)}…${s.slice(-(max / 2 - 1))}` : s)

/**
 * Reveal a stored value on demand, into its own small dialog. The value is fetched when the button
 * is pressed and dropped from memory when the dialog closes; it never sits in the list.
 */
export function RevealButton({ title, load }: { title: string; load: () => Promise<string> }) {
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  const reveal = async () => {
    setBusy(true)
    setError(undefined)
    try {
      setValue(await load())
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
      setOpen(true)
    }
  }

  const close = () => {
    setOpen(false)
    setValue(undefined)
  }

  return (
    <>
      <Button variant="ghost" size="iconSm" aria-label={`Reveal ${title}`} title="Reveal" disabled={busy} onClick={reveal}>
        <Eye />
      </Button>
      <Dialog open={open} onOpenChange={(o) => !o && close()}>
        <DialogContent onOpenAutoFocus={(e) => e.preventDefault()}>
          <DialogHeader>
            <DialogTitle className="break-words">{title}</DialogTitle>
            <DialogDescription>Copy the value and keep it out of transcripts, logs and commits.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            {error ? (
              <Callout tone="danger">{error}</Callout>
            ) : (
              <div className="flex items-start gap-2 rounded-lg border border-dashed border-border-strong bg-code p-3">
                <code className="min-w-0 flex-1 break-all font-mono text-xs leading-5 text-code-foreground">{value !== undefined ? middleTruncate(value) : ''}</code>
                {value !== undefined && <CopyButton value={value} label="Copy value" />}
              </div>
            )}
          </DialogBody>
          <DialogFooter>
            <Button variant="primary" onClick={close}>
              Done
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
