// Mirrors internal/httpapi/dto.go. Keep the field names identical to the Go JSON tags.

export type AuthKind = 'none' | 'oauth' | 'basic' | 'header'
export type CredentialState = 'ok' | 'pending' | 'broken'
export type Hop = 'inbound' | 'upstream'
export type Outcome = 'success' | 'error' | 'denied' | 'partial'
export type OAuthRegistrationKind = 'cimd' | 'dcr'

export type APIKeyScope = 'api:read' | 'api:edit' | 'mcp:execute' | 'mcp:edit'

export interface ConfigDTO {
  devMode: boolean
  oidcIssuer?: string
  oidcClientId?: string
  publicBaseUrl: string
  version: string
  mcpScope: string
  apiKeyScopes: string[]
}

export interface MeDTO {
  ownerId: string
  subject: string
  type: string
  email?: string
  name?: string
  scopes: string[]
}

export interface CredentialDTO {
  kind: AuthKind
  state: CredentialState
  stateReason?: string
  basicUsername?: string
  headerName?: string
  oauthIssuer?: string
  oauthClientId?: string
  oauthRegistrationKind?: OAuthRegistrationKind | string
  oauthResource?: string
  oauthScopes: string[]
  oauthExpiresAt?: string
  oauthHasRefreshToken: boolean
  lastRefreshedAt?: string
  lastError?: string
  updatedAt: string
}

export interface ServerDTO {
  id: string
  slug: string
  name: string
  url: string
  description: string
  authKind: AuthKind
  enabled: boolean
  createdAt: string
  updatedAt: string
  mcpUrl: string
  credential?: CredentialDTO
}

export interface AuthInput {
  kind: AuthKind
  username?: string
  password?: string
  headerName?: string
  headerValue?: string
  scopes?: string[]
}

export interface CreateServerInput {
  name: string
  url: string
  description: string
  auth: AuthInput
}

export interface UpdateServerInput {
  name?: string
  url?: string
  description?: string
  enabled?: boolean
}

export interface CreateServerResult {
  server: ServerDTO
  authorizeUrl?: string
}

export interface OAuthStartInput {
  scopes?: string[]
  returnTo?: string
}

export interface OAuthStartResult {
  authorizeUrl: string
}

export interface PingResult {
  ok: boolean
  latencyMs: number
  protocolVersion?: string
  serverName?: string
  serverVersion?: string
  toolCount: number
  error?: string
}

export interface APIKeyDTO {
  id: string
  name: string
  prefix: string
  scopes: string[]
  createdAt: string
  lastUsedAt?: string
  revokedAt?: string
}

export interface CreateAPIKeyInput {
  name: string
  scopes: string[]
}

export interface CreateAPIKeyResult {
  key: APIKeyDTO
  secret: string
}

export interface AuditEventDTO {
  id: string
  createdAt: string
  hop: Hop
  parentId?: string
  actorType: string
  actorSubject: string
  apiKeyId?: string
  apiKeyPrefix?: string
  serverId?: string
  slug?: string
  route: string
  httpMethod: string
  path: string
  mcpMethod?: string
  toolName?: string
  jsonrpcId?: string
  protocolVersion?: string
  statusCode: number
  outcome: Outcome
  requestBytes: number
  responseBytes: number
  durationMs: number
  requestId: string
  errorType?: string
  hasPayload: boolean
}

export interface AuditPayloadDTO {
  requestHeaders: Record<string, string>
  requestBody: string
  responseHeaders: Record<string, string>
  responseBody: string
}

export interface AuditDetailDTO {
  event: AuditEventDTO
  payload?: AuditPayloadDTO
}

export interface AuditTraceDTO {
  root?: AuditEventDTO
  spans: AuditEventDTO[]
}

export interface PageDTO<T> {
  items: T[]
  total: number
  offset: number
  limit: number
}

export interface AuditQuery {
  offset?: number
  limit?: number
  slug?: string
  hop?: Hop | ''
  method?: string
  tool?: string
  outcome?: Outcome | ''
  requestId?: string
  serverId?: string
}

export interface E2EInput {
  search?: boolean
  apiKeyId: string
  name?: string
  arguments?: Record<string, unknown>
}

export interface E2EResult<T = unknown> {
  ok: boolean
  latencyMs: number
  endpoint: string
  result?: T
  error?: string
  requestId?: string
}

// MCP shapes carried inside E2EResult.result.
export interface JSONSchema {
  type?: string | string[]
  title?: string
  description?: string
  properties?: Record<string, JSONSchema>
  required?: string[]
  enum?: unknown[]
  default?: unknown
  items?: JSONSchema
  format?: string
  minimum?: number
  maximum?: number
  [key: string]: unknown
}

export interface MCPTool {
  name: string
  description?: string
  inputSchema?: JSONSchema
}

export interface ListToolsResult {
  tools: MCPTool[]
}

export interface MCPContent {
  type: string
  text?: string
  data?: string
  mimeType?: string
  [key: string]: unknown
}

export interface CallToolResult {
  content?: MCPContent[]
  structuredContent?: unknown
  isError?: boolean
}
