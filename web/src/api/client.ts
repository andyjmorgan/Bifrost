import { getAccessToken, isDevMode, signOutOn401 } from '../auth'

/** An API failure with the HTTP status and the `{error}` message from the body when there is one. */
export class ApiError extends Error {
  status: number
  /** The parsed JSON error body, when there was one (e.g. `{error, problems}`). */
  body?: unknown
  constructor(status: number, message: string, body?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

const BASE = '/api/v1'

async function parseError(res: Response): Promise<ApiError> {
  let message = `${res.status} ${res.statusText}`.trim()
  let parsed: unknown
  try {
    const text = await res.text()
    if (text) {
      try {
        const body = JSON.parse(text) as { error?: string; message?: string }
        parsed = body
        message = body.error || body.message || text
      } catch {
        message = text
      }
    }
  } catch {
    // keep the status line
  }
  return new ApiError(res.status, message, parsed)
}

/** Fetch against /api/v1 with the bearer token, JSON parsing, and ApiError on non-2xx. */
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (!isDevMode()) {
    const token = await getAccessToken()
    if (token) headers.set('Authorization', `Bearer ${token}`)
  }
  const res = await fetch(`${BASE}${path}`, { ...init, headers })
  if (res.status === 401 && !isDevMode()) {
    signOutOn401()
    throw new ApiError(401, 'Your session has expired. Please sign in again.')
  }
  if (!res.ok) throw await parseError(res)
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

export type SSEHandler = (event: string, data: unknown, raw: string) => void

/** POST and consume a text/event-stream response, calling onEvent per event until the stream ends. */
export async function requestSSE(path: string, body: unknown, onEvent: SSEHandler): Promise<void> {
  const headers = new Headers({ Accept: 'text/event-stream', 'Content-Type': 'application/json' })
  if (!isDevMode()) {
    const token = await getAccessToken()
    if (token) headers.set('Authorization', `Bearer ${token}`)
  }
  const res = await fetch(`${BASE}${path}`, { method: 'POST', body: JSON.stringify(body), headers })
  if (res.status === 401 && !isDevMode()) {
    signOutOn401()
    throw new ApiError(401, 'Your session has expired. Please sign in again.')
  }
  if (!res.ok) throw await parseError(res)
  if (!res.body) return
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  const dispatch = (block: string) => {
    let event = 'message'
    const data: string[] = []
    for (const line of block.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
    }
    if (!data.length) return
    const text = data.join('\n')
    let parsed: unknown = text
    try {
      parsed = JSON.parse(text)
    } catch {
      // keep raw text
    }
    onEvent(event, parsed, block)
  }
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let idx: number
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      dispatch(buf.slice(0, idx))
      buf = buf.slice(idx + 2)
    }
  }
  if (buf.trim()) dispatch(buf)
}

export const http = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) }),
  put: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PUT', body: body === undefined ? undefined : JSON.stringify(body) }),
  patch: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PATCH', body: body === undefined ? undefined : JSON.stringify(body) }),
  delete: <T = void>(path: string) => request<T>(path, { method: 'DELETE' }),
}

/** Human-readable message for any thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return String(e)
}
