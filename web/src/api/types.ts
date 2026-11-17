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
  name: string
  url: string
  description: string
  authKind: AuthKind
  enabled: boolean
  /** A caller holding more than one of this server's entitlements gets none of them. */
  exclusive: boolean
  /** Capabilities the gateway has seen on this server. */
  seen: number
  createdAt: string
  updatedAt: string
  credential?: CredentialDTO
  entitlements: EntitlementRefDTO[]
}

export interface EntitlementRefDTO {
  id: string
  slug: string
  name: string
  mcpUrl: string
  grants: number
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
  exclusive?: boolean
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
  createdBy?: string
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
  /** How many of the entitlement's rules applied to the request. */
  ruleHitCount?: number
  /** The rules that applied; on a single event only, a listing carries the count alone. */
  rules?: RuleHitsDTO
}

/** One rule that applied to a request. */
export interface RuleHitDTO {
  /** The hook the rule ran on, e.g. "tools/call.pre". */
  hook: string
  /** The rule's name: its hook and the line it starts on ("tools/call.pre:2"). */
  rule: string
  /** The slug of the server whose rule it is. */
  server?: string
  /** The tool, prompt or resource it applied to. */
  item?: string
  mode: 'enforce' | 'audit'
  /** deny, elicit, set or error; "would …" for an audit rule. */
  effect: string
  /** What a set changed. */
  fields?: string[]
  reason?: string
  error?: string
}

export interface RuleHitsDTO {
  hits: RuleHitDTO[]
  /** How many more applied than are kept. */
  dropped?: number
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
  /** Omitted = the call goes through the front door as the signed-in user; present = act as that key. */
  apiKeyId?: string
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

// Entitlements: a named, grantable view of one server. Its slug is the route (/{slug}/mcp) and the
// mux prefix (slug__tool).

/**
 * Allow lists keyed by kind. A key present means only what it names (an empty array is none of that
 * kind); a key absent means everything of that kind. `{}` is everything the server offers.
 */
export type Selection = {
  tools?: string[]
  prompts?: string[]
  /** URI patterns: exact, * within a segment, ** prefix. */
  resources?: string[]
  /** uriTemplate strings. */
  templates?: string[]
}

export type SubjectType = 'user' | 'role'

export interface GrantDTO {
  subjectType: SubjectType
  subjectId: string
  email?: string
  name?: string
  grantedAt: string
}

export interface GrantInput {
  subjectType: SubjectType
  subjectId: string
}

export interface EntitlementDTO {
  id: string
  serverId: string
  serverName: string
  slug: string
  name: string
  description: string
  mcpUrl: string
  selection: Selection
  /** The text of each hook's rules, one property per hook. Every property is present; null means the hook has no rules. */
  hooks: HooksDTO
  /** Why the stored rules cannot be used. While set, the entitlement exposes nothing. */
  rulesError?: string
  generation: number
  grants: GrantDTO[]
  createdAt: string
  updatedAt: string
}

/** slug only on create (omitted = derived from name); ignored on PATCH. */
export interface EntitlementInput {
  name: string
  slug?: string
  description: string
  selection?: Selection
  /** Omit to leave unchanged. What is sent replaces all the rules: a property that is null or absent clears that hook. */
  hooks?: HooksDTO
}

/**
 * An entitlement's hooks: the text of each hook's rules, under the hook's property. The property is
 * the hook's name with the separators dropped and the parts joined in camel case, so
 * tools/call.post.result is toolsCallPostResult. Null or absent means the hook has no rules.
 */
export interface HooksDTO {
  serverSettings?: string | null
  serverSession?: string | null
  serverDiscoverPost?: string | null
  toolsListPost?: string | null
  toolsCallPre?: string | null
  toolsCallPostProgress?: string | null
  toolsCallPostElicit?: string | null
  toolsCallPostResult?: string | null
  promptsListPost?: string | null
  promptsGetPre?: string | null
  promptsGetPostProgress?: string | null
  promptsGetPostElicit?: string | null
  resourcesListPost?: string | null
  resourcesTemplatesListPost?: string | null
  resourcesReadPre?: string | null
  resourcesReadPostProgress?: string | null
  resourcesReadPostElicit?: string | null
  resourcesReadPostResult?: string | null
  skillsListPost?: string | null
}

/** A hook's property in HooksDTO. */
export type HookProperty = keyof HooksDTO

export type CapabilityKind = 'tool' | 'prompt' | 'resource' | 'template'

export interface CapabilityDTO {
  kind: CapabilityKind
  name: string
  fingerprint?: string
  firstSeenAt: string
  lastSeenAt: string
  /** The tool / prompt / resource / template object exactly as the server last sent it. */
  definition?: unknown
}

/** GET /entitlements/hooks-schema: every hook, described for someone writing rules on it. */
export interface HooksSchema {
  /** Flag name -> what it means; read in a rule as flags.<name>. */
  flags: Record<string, string>
  /** Key of `caller` -> what it holds; read in a rule as caller.<key>. */
  caller?: Record<string, string>
  /** Every hook, in the order a request meets them. */
  hooks: HookInfo[]
}

/** One hook: a place in a request where rules run. A method, a side of it and, for a reply, what came back. */
export interface HookInfo {
  /** The engine's name for the hook, e.g. "tools/call.post.result". */
  name: string
  /** The property the hook's rules are kept under, e.g. "toolsCallPostResult". */
  property: HookProperty
  /** Server, Tools, Prompts or Resources. */
  group: string
  /** e.g. "tools/call"; "server" for the two that are not a method. */
  method: string
  side: 'pre' | 'post' | 'settings' | 'session'
  event?: 'progress' | 'elicit' | 'result'
  /** What a rule here is about: tool, prompt, resource, template, skill or server. */
  item?: string
  summary: string
  notes?: string[]
  /** deny, confirm, set. */
  actions: string[]
  /** What deny does here, e.g. "hides the tool". */
  deny?: string
  /** Everything a rule here can read, with what it may set marked writable. */
  tree: TreeNode
  /** The paths a rule here may set: the tree's writable nodes. */
  sets: HookSet[]
  /** Functions only this hook's rules can call. */
  helpers?: string[]
  /** A full set of rules for the hook, to copy from. Every line compiles. */
  example: string
}

/** A problem in a hook's rules. `line` is 1-based; 0 is the hook's rules as a whole. `hook` is empty for a problem with the rules as a whole. */
export interface RuleProblem {
  /** The hook's name, e.g. "tools/call.pre". */
  hook: string
  line: number
  message: string
  /** "warning": the rule compiles but looks wrong, and does not stop a save. Absent: the rules cannot be used. */
  level?: 'warning'
}

/** POST /entitlements/validate: what the engine's compiler and the sanity checks found. */
export interface RulesCheck {
  problems: RuleProblem[]
  warnings: RuleProblem[]
  /** Whether the text compiled to any rule; false when it has problems or holds only comments. */
  hasRules: boolean
}

/**
 * One place in a hook's tree: what a rule can read there, to any depth. A hook's tree has the item
 * (tool, prompt, resource, ...), the request, what came back, the server and the entitlement.
 */
export interface TreeNode {
  /** string, bool, number, timestamp, duration, list, map or any. */
  type: string
  doc?: string
  /** The value may be absent. A rule reads it with .? or has(). */
  optional?: boolean
  /** A rule on this hook may set it. */
  writable?: boolean
  /** A rule sets it and cannot read it. */
  writeOnly?: boolean
  /** The values of a string that has a fixed set. */
  enum?: string[]
  /** The keys a map holds. */
  keys?: Record<string, TreeNode>
  /** The map may hold keys beyond `keys`; `values` is the form of each. */
  open?: boolean
  values?: TreeNode
  /** The open map's keys are names from the server's catalog: "parameters", "output" or "meta". */
  names?: string
  /** The value takes its form from one of the item's JSON Schemas: "inputSchema" or "outputSchema". */
  follows?: string
  /** The form of each element of a list. */
  items?: TreeNode
}

export interface HookSet {
  /** A path in the tree; "*" stands for any one name (tool.params.*.default). */
  path: string
  /** decision, string, bool, number, duration, list, map, result or any. */
  type: string
}

/** One slug the caller holds (the member portal). */
export interface ExposureDTO {
  slug: string
  name: string
  description: string
  mcpUrl: string
  serverId: string
  serverName: string
  serverUrl: string
  enabled: boolean
  selection: Selection
  /** "user" and/or "role:member" / "role:admin". */
  via: string[]
  /** Set when an exclusive server is held twice. */
  conflict?: string
  /** "kind:name" this slug exposes (seen on the server and inside the selection). */
  capabilities: string[]
  /** Whether that entitlement has rules on some hook. */
  hasRules: boolean
}
