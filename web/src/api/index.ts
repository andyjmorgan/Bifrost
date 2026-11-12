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
  DiscoverResult,
  E2EResult,
  ListToolsResult,
  ListPromptsResult,
  GetPromptResult,
  ListResourcesResult,
  ListResourceTemplatesResult,
  ReadResourceResult,
  MeDTO,
  InviteDTO,
  InvitePreviewDTO,
  OrgDTO,
  OrgRole,
  OAuthStartInput,
  OAuthStartResult,
  PageDTO,
  PingResult,
  ServerDTO,
  EntitlementDTO,
  EntitlementInput,
  GrantInput,
  CapabilityDTO,
  ExposureDTO,
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

  org: {
    get: () => http.get<OrgDTO>('/org'),
    rename: (name: string) => http.patch<OrgDTO>('/org', { name }),
    join: (token: string) => http.post<OrgDTO>('/org/join', { token }),
    leave: () => http.post<OrgDTO>('/org/leave'),
    switch: (orgId: string) => http.post<OrgDTO>('/org/switch', { orgId }),
    setRole: (userId: string, role: OrgRole) => http.patch<OrgDTO>(`/org/members/${encodeURIComponent(userId)}`, { role }),
    /** OrgDTO, or undefined (204) when an admin removed themselves and is now in their personal org. */
    removeMember: (userId: string) => http.delete<OrgDTO | undefined>(`/org/members/${encodeURIComponent(userId)}`),
    invites: {
      list: () => http.get<InviteDTO[]>('/org/invites'),
      create: (role: OrgRole = 'member') => http.post<InviteDTO>('/org/invites', { role }),
      revoke: (id: string) => http.delete(`/org/invites/${encodeURIComponent(id)}`),
      preview: (token: string) => http.get<InvitePreviewDTO>(`/org/invites/${encodeURIComponent(token)}`),
    },
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
  },

  e2e: {
    listTools: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ListToolsResult>(`/e2e/${encodeURIComponent(route)}/tools/list`, input, onEvent),
    callTool: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<CallToolResult>(`/e2e/${encodeURIComponent(route)}/tools/call`, input, onEvent),
    listPrompts: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ListPromptsResult>(`/e2e/${encodeURIComponent(route)}/prompts/list`, input, onEvent),
    getPrompt: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<GetPromptResult>(`/e2e/${encodeURIComponent(route)}/prompts/get`, input, onEvent),
    listResources: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ListResourcesResult>(`/e2e/${encodeURIComponent(route)}/resources/list`, input, onEvent),
    listResourceTemplates: (route: string, input: E2EInput, onEvent?: E2EEventHandler) =>
      e2eStream<ListResourceTemplatesResult>(`/e2e/${encodeURIComponent(route)}/resources/templates`, input, onEvent),
    readResource: (route: string, input: E2EInput, onEvent?: E2EEventHandler) => e2eStream<ReadResourceResult>(`/e2e/${encodeURIComponent(route)}/resources/read`, input, onEvent),
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

  exposures: () => http.get<ExposureDTO[]>('/exposures'),

  entitlements: {
    list: () => http.get<EntitlementDTO[]>('/entitlements'),
    listForServer: (serverId: string) => http.get<EntitlementDTO[]>(`/servers/${encodeURIComponent(serverId)}/entitlements`),
    create: (serverId: string, input: EntitlementInput) => http.post<EntitlementDTO>(`/servers/${encodeURIComponent(serverId)}/entitlements`, input),
    get: (id: string) => http.get<EntitlementDTO>(`/entitlements/${encodeURIComponent(id)}`),
    update: (id: string, input: EntitlementInput) => http.patch<EntitlementDTO>(`/entitlements/${encodeURIComponent(id)}`, input),
    remove: (id: string) => http.delete(`/entitlements/${encodeURIComponent(id)}`),
    putGrants: (id: string, grants: GrantInput[]) => http.put<EntitlementDTO>(`/entitlements/${encodeURIComponent(id)}/grants`, { grants }),
  },

  capabilities: {
    list: (serverId: string) => http.get<CapabilityDTO[]>(`/servers/${encodeURIComponent(serverId)}/capabilities`),
  },
}
