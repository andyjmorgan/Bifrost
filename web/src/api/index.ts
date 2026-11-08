import { requestSSE, http } from './client'
import type {
  APIKeyDTO,
  AuditDetailDTO,
  AuditTraceDTO,
  AuditEventDTO,
  AuditQuery,
  AuthInput,
  CallToolResult,
  CreateAPIKeyInput,
  CreateAPIKeyResult,
  CreateServerInput,
  CreateServerResult,
  E2EInput,
  E2EResult,
  ListToolsResult,
  MeDTO,
  OAuthStartInput,
  OAuthStartResult,
  PageDTO,
  PingResult,
  ServerDTO,
  UpdateServerInput,
} from './types'

export * from './types'
export { ApiError, errorMessage } from './client'

function qs(params: Record<string, string | number | undefined>): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') p.set(k, String(v))
  }
  const s = p.toString()
  return s ? `?${s}` : ''
}

export interface ProgressEvent {
  progress: number
  total?: number
  message?: string
}
export interface LogEvent {
  level: string
  logger?: string
  data: unknown
}
export type E2EEventHandler = (e: { type: 'progress'; data: ProgressEvent; raw: string } | { type: 'log'; data: LogEvent; raw: string } | { type: 'result'; raw: string }) => void

/** The e2e endpoints stream SSE: progress/log notifications from the upstream, then one `result` event. */
async function e2eStream<T>(path: string, input: E2EInput, onEvent?: E2EEventHandler): Promise<E2EResult<T>> {
  let result: E2EResult<T> | undefined
  await requestSSE(path, input, (event, data, raw) => {
    if (event === 'result') {
      result = data as E2EResult<T>
      onEvent?.({ type: 'result', raw })
    } else if (event === 'progress') onEvent?.({ type: 'progress', data: data as ProgressEvent, raw })
    else if (event === 'log') onEvent?.({ type: 'log', data: data as LogEvent, raw })
  })
  if (!result) throw new Error('The stream ended without a result.')
  return result
}

export const api = {
  me: () => http.get<MeDTO>('/me'),

  servers: {
    list: () => http.get<ServerDTO[]>('/servers'),
    get: (id: string) => http.get<ServerDTO>(`/servers/${encodeURIComponent(id)}`),
    create: (input: CreateServerInput) => http.post<CreateServerResult>('/servers', input),
    update: (id: string, input: UpdateServerInput) => http.patch<ServerDTO>(`/servers/${encodeURIComponent(id)}`, input),
    remove: (id: string) => http.delete(`/servers/${encodeURIComponent(id)}`),
    putCredential: (id: string, input: AuthInput) => http.put<CreateServerResult>(`/servers/${encodeURIComponent(id)}/credential`, input),
    oauthStart: (id: string, input: OAuthStartInput) => http.post<OAuthStartResult>(`/servers/${encodeURIComponent(id)}/oauth/start`, input),
    ping: (id: string) => http.post<PingResult>(`/servers/${encodeURIComponent(id)}/ping`),
  },

  e2e: {
    listTools: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ListToolsResult>(`/e2e/${encodeURIComponent(route)}/tools/list`, input, onEvent),
    callTool: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<CallToolResult>(`/e2e/${encodeURIComponent(route)}/tools/call`, input, onEvent),
  },

  apiKeys: {
    list: () => http.get<APIKeyDTO[]>('/api-keys'),
    create: (input: CreateAPIKeyInput) => http.post<CreateAPIKeyResult>('/api-keys', input),
    revoke: (id: string) => http.delete(`/api-keys/${encodeURIComponent(id)}`),
  },

  audit: {
    list: (q: AuditQuery) => http.get<PageDTO<AuditEventDTO>>(`/audit${qs(q as Record<string, string | number | undefined>)}`),
    get: (id: string) => http.get<AuditDetailDTO>(`/audit/${encodeURIComponent(id)}`),
    trace: (id: string) => http.get<AuditTraceDTO>(`/audit/${encodeURIComponent(id)}/trace`),
  },
}
