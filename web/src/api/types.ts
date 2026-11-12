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

export type OrgRole = 'admin' | 'member'

export interface MeDTO {
  userId: string
  orgId: string
  orgName: string
  /** The active org is the caller's own personal org. */
  personalOrg: boolean
  role: OrgRole
  subject: string
  /** 'oidc' | 'apikey' | 'dev' */
  type: string
  email?: string
  name?: string
  scopes: string[]
}

export interface OrgRefDTO {
  id: string
  name: string
  role: OrgRole
  personal: boolean
  active: boolean
}

export interface MemberDTO {
  userId: string
  email: string
  name: string
  role: OrgRole
  joinedAt: string
  self: boolean
}

export interface OrgDTO {
  id: string
  name: string
  personal: boolean
  role: OrgRole
  members: MemberDTO[]
  /** Every org the caller belongs to, personal first. */
  orgs: OrgRefDTO[]
  /** e.g. "https://bifrost.donkeywork.dev/join/" */
  inviteBase: string
}

export interface InviteDTO {
  id: string
  role: OrgRole
  /** user id */
  createdBy: string
  createdAt: string
  expiresAt: string
  usedAt?: string
  usedBy?: string
  /** Only on the create response; shown once, then gone. */
  url?: string
}

export interface InvitePreviewDTO {
  orgName: string
  role: OrgRole
  expiresAt: string
  member: boolean
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

export interface DiscoverResult {
  ok: boolean
  latencyMs: number
  protocolVersion?: string
  serverInfo?: { name: string; title?: string; description?: string; version: string; websiteUrl?: string; icons?: unknown[] }
  capabilities?: Record<string, unknown>
  instructions?: string
  _meta?: Record<string, unknown>
  error?: string
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
  uri?: string
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

export interface MCPToolAnnotations {
  title?: string
  readOnlyHint?: boolean
  destructiveHint?: boolean
  idempotentHint?: boolean
  openWorldHint?: boolean
}

export interface MCPTool {
  name: string
  title?: string
  description?: string
  inputSchema?: JSONSchema
  outputSchema?: JSONSchema
  annotations?: MCPToolAnnotations
  _meta?: Record<string, unknown>
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

export interface MCPIcon {
  src: string
  mimeType?: string
  sizes?: string[]
  [key: string]: unknown
}

export interface MCPPromptArgument {
  name: string
  title?: string
  description?: string
  required?: boolean
}

export interface MCPPrompt {
  name: string
  title?: string
  description?: string
  arguments?: MCPPromptArgument[]
  icons?: MCPIcon[]
  _meta?: Record<string, unknown>
}

export interface ListPromptsResult {
  prompts: MCPPrompt[]
  _meta?: Record<string, unknown>
}

export interface MCPResourceContents {
  uri: string
  mimeType?: string
  text?: string
  blob?: string
  _meta?: Record<string, unknown>
}

export type MCPPromptContent =
  | { type: 'text'; text: string }
  | { type: 'image' | 'audio'; data: string; mimeType: string }
  | { type: 'resource'; resource: MCPResourceContents }
  | { type: 'resource_link'; uri: string; name?: string; title?: string; description?: string; mimeType?: string }

export interface MCPPromptMessage {
  role: 'user' | 'assistant'
  content: MCPPromptContent
}

export interface GetPromptResult {
  description?: string
  messages: MCPPromptMessage[]
  _meta?: Record<string, unknown>
}

export interface MCPResourceAnnotations {
  audience?: string[]
  priority?: number
  lastModified?: string
}

export interface MCPResource {
  uri: string
  name: string
  title?: string
  description?: string
  mimeType?: string
  size?: number
  annotations?: MCPResourceAnnotations
  _meta?: Record<string, unknown>
}

export interface ListResourcesResult {
  resources: MCPResource[]
  _meta?: Record<string, unknown>
}

export interface MCPResourceTemplate {
  uriTemplate: string
  name: string
  title?: string
  description?: string
  mimeType?: string
  annotations?: MCPResourceAnnotations
  _meta?: Record<string, unknown>
}

export interface ListResourceTemplatesResult {
  resourceTemplates: MCPResourceTemplate[]
  _meta?: Record<string, unknown>
}

export interface ReadResourceResult {
  contents: MCPResourceContents[]
  _meta?: Record<string, unknown>
}

// Policies: ordered CEL rules that shape what a route exposes. A route is "mux" (the global /mcp)
// or a server slug.
export interface PolicyRule {
  id: string
  name: string
  enabled?: boolean
  when: string
  then: Record<string, string>
}

export interface PolicyBindingRef {
  route: string
  serverId?: string
  serverName?: string
}

export interface PolicyDTO {
  id: string
  name: string
  description: string
  invert: boolean
  rules: PolicyRule[]
  createdAt: string
  updatedAt: string
  bindings: PolicyBindingRef[]
}

export interface PolicyInput {
  name: string
  description: string
  invert: boolean
  rules: PolicyRule[]
}

export interface PolicyField {
  name: string
  kind: 'bool' | 'string'
  doc: string
}

export interface PolicyProblem {
  rule: number
  field?: string
  message: string
}

export interface ValidatePolicyResult {
  problems: PolicyProblem[]
}

export interface PolicyHit {
  policy: string
  rule: string
  set?: string[]
  error?: string
}

/** The shaped tool definition as the client will see it. */
export type Tool = MCPTool & { [key: string]: unknown }

export interface PolicyPreviewTool {
  server: string
  upstream: string
  name: string
  visible: boolean
  confirm: boolean
  confirmMessage?: string
  changed: string[]
  hits: PolicyHit[]
  tool: Tool
}

export interface PolicyPreviewInput {
  policy: PolicyInput
  route: string
  extra?: boolean
}

export interface PolicyPreviewResult {
  route: string
  tools: PolicyPreviewTool[]
  unavailable?: { server: string; error: string }[]
}

export interface RouteBindingsDTO {
  route: string
  serverId?: string
  serverName?: string
  policies: { id: string; name: string }[]
}
