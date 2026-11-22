import { useEffect, useRef, useState } from 'react'
import { CircleStop, MessageSquareText } from 'lucide-react'
import { Button } from '../../ui/components/button'
import { Badge } from '../../ui/components/badge'
import { StatusPill, type Tone } from '../../components/StatusPill'
import { Callout } from '../../components/Callout'
import { ElicitationDialog, type ElicitResponse } from '../../components/elicitation/ElicitationDialog'
import { api, errorMessage, type CallToolResult, type E2EResult, type TaskResult } from '../../api'
import { JsonView, RequestLink, TextOrJson } from './shared'

const statusTone: Record<string, Tone> = {
  working: 'info',
  input_required: 'warning',
  completed: 'success',
  failed: 'danger',
  cancelled: 'muted',
}

const finished = (s?: string) => s === 'completed' || s === 'failed' || s === 'cancelled'

/** One line of the task's history: what the client was told, when. */
interface Poll {
  at: string
  method: string
  status?: string
  message?: string
  requestId?: string
  error?: string
}

/**
 * Follows a task the gateway handed back for a tools/call: polls tasks/get at the task's interval,
 * shows each status, asks the task's questions as forms (answered with tasks/update), and cancels on
 * request. Every poll goes through the front door as the same caller, so it lands in the audit trail.
 */
export function TaskPanel({ route, keyId, start, raw, tool }: { route: string; keyId: string; start: E2EResult<TaskResult>; raw: boolean; tool?: string }) {
  const first = start.result
  const taskId = first?.taskId ?? ''
  const [task, setTask] = useState<TaskResult | undefined>(first)
  const [polls, setPolls] = useState<Poll[]>(() => [
    { at: new Date().toLocaleTimeString(), method: 'tools/call', status: first?.status, message: first?.statusMessage, requestId: start.requestId },
  ])
  // The keys of the questions the user closed without answering stay closed until asked again.
  const [dismissed, setDismissed] = useState<string>()
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<string>()
  const [pollError, setPollError] = useState<string>()
  const timer = useRef<number | undefined>(undefined)
  const done = finished(task?.status) || !!pollError

  const input = { apiKeyId: keyId || undefined, taskId }
  const record = (method: string, r: E2EResult<TaskResult>) =>
    setPolls((p) => [
      ...p,
      { at: new Date().toLocaleTimeString(), method, status: r.result?.status, message: r.result?.statusMessage, requestId: r.requestId, error: r.ok ? undefined : r.error },
    ])

  // Poll while the task runs. A question pauses nothing: the task keeps polling until it is answered.
  useEffect(() => {
    if (!taskId || done) return
    const wait = Math.max(500, Math.min(task?.pollIntervalMs ?? 1000, 5000))
    timer.current = window.setTimeout(async () => {
      try {
        const r = await api.e2e.taskGet(route, input)
        record('tasks/get', r)
        if (!r.ok) setPollError(r.error ?? 'tasks/get failed')
        else setTask(r.result)
      } catch (e) {
        setPollError(errorMessage(e))
      }
    }, wait)
    return () => window.clearTimeout(timer.current)
  }, [taskId, task, done])

  const questions = Object.entries(task?.status === 'input_required' ? (task.inputRequests ?? {}) : {})
  const questionKeys = questions.map(([k]) => k).join(',')
  const askNow = questions.length > 0 && dismissed !== questionKeys && !sending

  const answer = async (responses: Record<string, ElicitResponse>) => {
    setSendError(undefined)
    setSending(true)
    try {
      const r = await api.e2e.taskUpdate(route, { ...input, inputResponses: responses })
      record('tasks/update', r)
      if (!r.ok) setSendError(r.error)
      // Clear the questions until the next poll says what the task is doing now.
      else setTask((t) => (t ? { ...t, status: 'working', inputRequests: undefined } : t))
    } catch (e) {
      setSendError(errorMessage(e))
    } finally {
      setSending(false)
    }
  }

  const cancel = async () => {
    setSending(true)
    try {
      const r = await api.e2e.taskCancel(route, input)
      record('tasks/cancel', r)
      if (r.ok) setTask((t) => ({ ...t, status: 'cancelled' }))
      else setSendError(r.error)
    } catch (e) {
      setSendError(errorMessage(e))
    } finally {
      setSending(false)
    }
  }

  const status = task?.status ?? 'working'
  return (
    <div className="rounded-2xl border border-border bg-card" data-testid="task-panel">
      <div className="flex flex-wrap items-center gap-3 border-b border-border p-5 text-xs text-muted-foreground">
        <StatusPill tone={pollError ? 'danger' : (statusTone[status] ?? 'muted')}>{pollError ? 'Poll failed' : `Task ${status.replace('_', ' ')}`}</StatusPill>
        <span className="font-mono">{taskId}</span>
        {!done && (
          <Button variant="ghost" size="sm" onClick={cancel} disabled={sending} className="ml-auto">
            <CircleStop /> Cancel task
          </Button>
        )}
      </div>
      <div className="space-y-4 p-5">
        {task?.statusMessage && !finished(status) && <p className="text-sm">{task.statusMessage}</p>}
        {pollError && (
          <Callout tone="danger">
            <span className="break-words font-mono text-xs">{pollError}</span>
          </Callout>
        )}

        {questions.length > 0 && (
          <div className="flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/5 p-4">
            <MessageSquareText className="size-4 text-warning" aria-hidden />
            <p className="min-w-0 flex-1 text-sm">{questions[0][1].params?.message ?? 'The task is waiting for your answer.'}</p>
            <Button variant="secondary" size="sm" onClick={() => setDismissed(undefined)} disabled={sending}>
              Answer
            </Button>
          </div>
        )}
        <ElicitationDialog
          requests={askNow ? questions : undefined}
          source={`${tool ?? 'task'} · ${taskId}`}
          onDone={(responses) => {
            // Closing without answering (every answer a cancel) leaves the task waiting.
            if (Object.values(responses).every((r) => r.action === 'cancel')) setDismissed(questionKeys)
            else void answer(responses)
          }}
        />
        {sendError && (
          <Callout tone="danger">
            <span className="break-words font-mono text-xs">{sendError}</span>
          </Callout>
        )}

        {status === 'completed' && task?.result && <TaskOutcome result={task.result} raw={raw} />}
        {status === 'failed' && (
          <Callout tone="danger">
            <span className="break-words text-xs">{task?.error?.message ?? task?.statusMessage ?? 'The task failed.'}</span>
          </Callout>
        )}
        {raw && task && <JsonView value={task} />}

        <details className="text-xs" open>
          <summary className="cursor-pointer select-none text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            Exchanges ({polls.length})
          </summary>
          <ol className="mt-2 max-h-56 space-y-1 overflow-auto font-mono" data-testid="task-polls">
            {polls.map((p, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-x-3">
                <span className="text-muted-foreground">{p.at}</span>
                <span>{p.method}</span>
                {p.status && <Badge mono>{p.status}</Badge>}
                {p.message && <span className="text-muted-foreground">{p.message}</span>}
                {p.error && <span className="text-danger">{p.error}</span>}
                {p.requestId && (
                  <span className="ml-auto">
                    <RequestLink id={p.requestId} />
                  </span>
                )}
              </li>
            ))}
          </ol>
        </details>
      </div>
    </div>
  )
}

function TaskOutcome({ result, raw }: { result: CallToolResult; raw: boolean }) {
  if (raw) return null
  const content = result.content ?? []
  return (
    <div className="space-y-3">
      {result.isError && <Callout tone="danger">The tool reported an error. Its content is shown below.</Callout>}
      {content.map((c, i) => (
        <div key={i} className="space-y-1.5">
          <Badge mono>{c.type}</Badge>
          {c.type === 'text' ? <TextOrJson text={c.text ?? ''} /> : <JsonView value={c} />}
        </div>
      ))}
      {result.structuredContent !== undefined && <JsonView value={result.structuredContent} />}
    </div>
  )
}
