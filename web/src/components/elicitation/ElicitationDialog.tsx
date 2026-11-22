import { useEffect, useMemo, useState } from 'react'
import { ExternalLink, Link2, MessageSquareText } from 'lucide-react'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../../ui/components/dialog'
import { Button } from '../../ui/components/button'
import { Badge } from '../../ui/components/badge'
import { Callout } from '../Callout'
import { IconTile } from '../IconTile'
import type { TaskInputRequest } from '../../api'
import { ElicitationForm, initialValues, toContent, validate, type Errors, type Values } from './ElicitationForm'

/** What the client answers to one question, as inputResponses carries it. */
export type ElicitResponse = { action: 'accept'; content?: Values } | { action: 'decline' } | { action: 'cancel' }

/**
 * Asks the questions a reply came back with (inputRequests), one at a time, and hands back an
 * answer for each. Accept sends the form; Decline says no; closing the dialog cancels. A link
 * request (URL mode) shows where it leads and opens it in a new tab.
 */
export function ElicitationDialog({
  requests,
  source,
  onDone,
}: {
  /** The questions, by key, in the order the reply listed them. */
  requests: [string, TaskInputRequest][] | undefined
  /** Who is asking, shown under the title: the tool, and the server or task. */
  source?: string
  onDone: (responses: Record<string, ElicitResponse>) => void
}) {
  const open = !!requests?.length
  const [step, setStep] = useState(0)
  const [answers, setAnswers] = useState<Record<string, ElicitResponse>>({})
  const [values, setValues] = useState<Values>({})
  const [errors, setErrors] = useState<Errors>({})
  const [opened, setOpened] = useState(false)
  // After the first Submit, errors follow the values as they change.
  const [tried, setTried] = useState(false)

  const current = requests?.[step]
  const [key, req] = current ?? ['', undefined]
  const params = req?.params
  const isForm = req?.method === 'elicitation/create' && params?.mode !== 'url'
  const isURL = req?.method === 'elicitation/create' && params?.mode === 'url'
  const url = typeof params?.url === 'string' ? params.url : undefined

  // A new set of questions starts from the first.
  const sig = requests?.map(([k]) => k).join(',') ?? ''
  useEffect(() => {
    setStep(0)
    setAnswers({})
  }, [sig])
  // Each question starts from its defaults.
  useEffect(() => {
    setValues(initialValues(params?.requestedSchema))
    setErrors({})
    setTried(false)
    setOpened(false)
  }, [sig, step])

  const answer = (r: ElicitResponse) => {
    const all = { ...answers, [key]: r }
    if (requests && step + 1 < requests.length) {
      setAnswers(all)
      setStep(step + 1)
    } else onDone(all)
  }

  const accept = () => {
    if (isForm) {
      const errs = validate(params?.requestedSchema, values)
      setErrors(errs)
      setTried(true)
      if (Object.keys(errs).length) return
      answer({ action: 'accept', content: toContent(params?.requestedSchema, values) })
    } else answer({ action: 'accept' })
  }

  const host = useMemo(() => {
    try {
      return url ? new URL(url).host : undefined
    } catch {
      return undefined
    }
  }, [url])

  return (
    <Dialog open={open} onOpenChange={(o) => !o && answer({ action: 'cancel' })}>
      <DialogContent className="max-w-xl" onOpenAutoFocus={(e) => e.preventDefault()} data-testid="elicitation-dialog">
        <form
          className="contents"
          onSubmit={(e) => {
            e.preventDefault()
            accept()
          }}
          noValidate
        >
          <DialogHeader>
            <div className="flex items-start gap-3">
              <IconTile icon={isURL ? Link2 : MessageSquareText} />
              <div className="min-w-0">
                <DialogTitle className="text-lg leading-6">{params?.message ?? 'Input requested'}</DialogTitle>
                <DialogDescription className="flex flex-wrap items-center gap-2">
                  {source && <span className="font-mono">{source}</span>}
                  {requests && requests.length > 1 && (
                    <Badge mono>
                      {step + 1} of {requests.length}
                    </Badge>
                  )}
                </DialogDescription>
              </div>
            </div>
          </DialogHeader>
          <DialogBody>
            {isForm && <ElicitationForm
                schema={params?.requestedSchema}
                values={values}
                errors={errors}
                onChange={(v) => {
                  setValues(v)
                  if (tried) setErrors(validate(params?.requestedSchema, v))
                }}
              />}
            {isURL && (
              <div className="space-y-4">
                <p className="text-sm text-muted-foreground">
                  This opens a page outside Bifrost{host ? ' at ' : ''}
                  {host && <span className="font-mono text-foreground">{host}</span>}. Check the address before you continue.
                </p>
                <div className="break-all rounded-lg border border-border bg-code p-3 font-mono text-xs text-code-foreground">{url ?? 'No URL was given.'}</div>
                {url && (
                  <Button type="button" variant="secondary" onClick={() => (window.open(url, '_blank', 'noopener,noreferrer'), setOpened(true))}>
                    <ExternalLink /> Open page
                  </Button>
                )}
              </div>
            )}
            {!isForm && !isURL && req && (
              <Callout tone="warning">
                The server asks for <span className="font-mono">{req.method}</span>, which the Explorer cannot answer. Decline or cancel to go on.
              </Callout>
            )}
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="ghost" className="mr-auto" onClick={() => answer({ action: 'decline' })}>
              Decline
            </Button>
            <Button type="button" variant="outline" onClick={() => answer({ action: 'cancel' })}>
              Cancel
            </Button>
            {(isForm || isURL) && (
              <Button type="submit" variant="primary">
                {isURL ? (opened ? "I've finished" : 'Continue') : requests && step + 1 < requests.length ? 'Next' : 'Submit'}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
