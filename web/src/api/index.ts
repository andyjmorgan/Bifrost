import { requestSSE, http } from './client'
import type {
  APIKeyDTO,
  AuditDetailDTO,
  AuditTraceDTO,
  AuditEventDTO,
  AuditQuery,
  AuthInput,
  CreateAPIKeyInput,
  CreateAPIKeyResult,
  CreateServerInput,
  CreateServerResult,
  E2EInput,
  DiscoverResult,
  E2EResult,
  ListToolsResult,
  ListPromptsResult,
  GetPromptResult,
  ListResourcesResult,
  ListResourceTemplatesResult,
  ReadResourceResult,
  ListSkillsResult,
  TaskResult,
  MeDTO,
  OAuthStartInput,
  OAuthStartResult,
  PageDTO,
  PingResult,
  ServerDTO,
  GrantInput,
  CapabilityDTO,
  PortalServerDTO,
  HooksDTO,
  RulesCheck,
  HooksSchema,
  UpdateServerInput,
  UserDTO,
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

  users: {
    /** Everyone who has signed in. */
    list: () => http.get<UserDTO[]>('/users'),
  },

  servers: {
    list: () => http.get<ServerDTO[]>('/servers'),
    get: (id: string) => http.get<ServerDTO>(`/servers/${encodeURIComponent(id)}`),
    create: (input: CreateServerInput) => http.post<CreateServerResult>('/servers', input),
    update: (id: string, input: UpdateServerInput) => http.patch<ServerDTO>(`/servers/${encodeURIComponent(id)}`, input),
    remove: (id: string) => http.delete(`/servers/${encodeURIComponent(id)}`),
    putCredential: (id: string, input: AuthInput) => http.put<CreateServerResult>(`/servers/${encodeURIComponent(id)}/credential`, input),
    oauthStart: (id: string, input: OAuthStartInput) => http.post<OAuthStartResult>(`/servers/${encodeURIComponent(id)}/oauth/start`, input),
    ping: (id: string) => http.post<PingResult>(`/servers/${encodeURIComponent(id)}/ping`),
    discover: (id: string) => http.post<DiscoverResult>(`/servers/${encodeURIComponent(id)}/discover`),
    putGrants: (id: string, grants: GrantInput[]) => http.put<ServerDTO>(`/servers/${encodeURIComponent(id)}/grants`, { grants }),
  },

  e2e: {
    listTools: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ListToolsResult>(`/e2e/${encodeURIComponent(route)}/tools/list`, input, onEvent),
    callTool: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<TaskResult>(`/e2e/${encodeURIComponent(route)}/tools/call`, input, onEvent),
    listPrompts: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ListPromptsResult>(`/e2e/${encodeURIComponent(route)}/prompts/list`, input, onEvent),
    getPrompt: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<GetPromptResult>(`/e2e/${encodeURIComponent(route)}/prompts/get`, input, onEvent),
    listResources: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ListResourcesResult>(`/e2e/${encodeURIComponent(route)}/resources/list`, input, onEvent),
    listResourceTemplates: (route: string, input: E2EInput, onEvent?: E2EEventHandler) =>
      e2eStream<ListResourceTemplatesResult>(`/e2e/${encodeURIComponent(route)}/resources/templates`, input, onEvent),
    readResource: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ReadResourceResult>(`/e2e/${encodeURIComponent(route)}/resources/read`, input, onEvent),
    listSkills: (route: string, input: E2EInput) => e2eStream<ListSkillsResult>(`/e2e/${encodeURIComponent(route)}/skills/list`, input),
    taskGet: (route: string, input: E2EInput) => e2eStream<TaskResult>(`/e2e/${encodeURIComponent(route)}/tasks/get`, input),
    taskUpdate: (route: string, input: E2EInput) => e2eStream<TaskResult>(`/e2e/${encodeURIComponent(route)}/tasks/update`, input),
    taskCancel: (route: string, input: E2EInput) => e2eStream<TaskResult>(`/e2e/${encodeURIComponent(route)}/tasks/cancel`, input),
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

  /** Every server the caller has been granted. */
  portal: () => http.get<PortalServerDTO[]>('/portal'),

  rules: {
    /** Checks rule text with the engine's compiler, then the sanity checks. */
    validate: (hooks: HooksDTO, catalog?: Record<string, string[]>) => http.post<RulesCheck>('/rules/validate', { hooks, catalog }),
    hooksSchema: () => http.get<HooksSchema>('/rules/hooks-schema'),
  },

  capabilities: {
    list: (serverId: string) => http.get<CapabilityDTO[]>(`/servers/${encodeURIComponent(serverId)}/capabilities`),
  },
}
