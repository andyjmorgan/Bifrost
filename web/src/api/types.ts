// Mirrors internal/httpapi/dto.go. Keep the field names identical to the Go JSON tags.

/**
 * How the gateway authenticates to a server. The two keyring kinds send a credential that lives in
 * the keyring (an OAuth account or a stored secret); the gateway keeps only its name. They exist
 * only when the keyring is enabled.
 */
export type AuthKind = 'none' | 'oauth' | 'basic' | 'header' | 'keyring_oauth' | 'keyring_secret'
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
  userId: string
  subject: string
  /** 'oidc' | 'apikey' | 'dev' */
  type: string
  email?: string
  name?: string
  scopes: string[]
}

/** GET /users: everyone who has signed in. */
export interface UserDTO {
  id: string
  email: string
  name: string
  lastSeenAt: string
}

export interface CredentialDTO {
  /** The credential as it was set. A Basic or header credential kept in the keyring is still basic or header. */
  kind: AuthKind
  /** Where the secret is held: the keyring, or the gateway's own store for one not moved yet. Absent for none. */
  store?: 'keyring' | 'gateway'
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
  /**
   * Keyring kinds: the user whose keyring holds the credential (the person who set it). For basic,
   * header and oauth kept in the keyring it is the gateway's own fixed id, which is no person.
   */
  keyringOwner?: string
  /** keyring_oauth: the provider and account. Names only; the token is never returned. */
  keyringProvider?: string
  keyringAccount?: string
  /** keyring_secret: the secret's name. The value is never returned. */
  keyringSecretName?: string
  lastRefreshedAt?: string
  lastError?: string
  updatedAt: string
}

export interface ServerDTO {
  id: string
  /** The route (/{slug}/mcp) and the mux prefix (slug__tool). Fixed at creation. */
  slug: string
  name: string
  url: string
  description: string
  authKind: AuthKind
  enabled: boolean
  /** Capabilities the gateway has seen on this server. */
  seen: number
  mcpUrl: string
  selection: Selection
  /** The text of each hook's rules, one property per hook. Every property is present; null means the hook has no rules. */
  hooks: HooksDTO
  /** Why the stored rules cannot be used. While set, the server exposes nothing. */
  rulesError?: string
  generation: number
  grants: GrantDTO[]
  credential?: CredentialDTO
  createdAt: string
  updatedAt: string
}

export interface AuthInput {
  kind: AuthKind
  username?: string
  password?: string
  headerName?: string
  headerValue?: string
  scopes?: string[]
  /** keyring_oauth: an OAuth account in the caller's own keyring. */
  keyringProvider?: string
  keyringAccount?: string
  /** keyring_secret: the name of a secret in the caller's own keyring. */
  keyringSecretName?: string
}

export interface CreateServerInput {
  name: string
  url: string
  description: string
  /** Omitted or empty = derived from the name. */
  slug?: string
  auth: AuthInput
}

export interface UpdateServerInput {
  name?: string
  url?: string
  description?: string
  enabled?: boolean
  selection?: Selection
  /** Omit to leave unchanged. What is sent replaces all the rules: a property that is null or absent clears that hook. */
  hooks?: HooksDTO
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
  /** How many of the server's rules applied to the request. */
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

/** The keyring's audit rows written for one gateway request: the credentials fetched to forward it. */
export interface KeyringAuditDTO {
  /** The id both trails carry for the request. The keyring's own audit list filters on it too. */
  requestId: string
  /** Empty when the request used no keyring credential, or the gateway has no keyring. */
  items: KeyringAuditRowDTO[]
}

/** One keyring audit row: which credential was read or refreshed, and whose it is. Names only. */
export interface KeyringAuditRowDTO {
  id: string
  /** As the keyring writes it: "TokenAccessed", "TokenRefreshed". */
  type: string
  /** "Success" or "Failure". */
  outcome: string
  /** The keyring user the credential belongs to. */
  owner: string
  /** The credential is the server's own: held in the keyring for the server, so it is in no person's keyring trail. */
  heldByGateway: boolean
  targetKind?: string
  targetProvider?: string
  targetAccount?: string
  targetName?: string
  method?: string
  detail?: string
  createdAt: string
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
  /** The gateway's task id, for tasks/get, tasks/update and tasks/cancel. */
  taskId?: string
  /** Answers to the questions a reply asked, by key: on tasks/update, or on the retry of a tools/call. */
  inputResponses?: Record<string, unknown>
  /** The state a tools/call that came back input_required handed out; the retry sends it back. */
  requestState?: string
  /** Declare the tasks extension on tools/call, so the server may answer with a task. */
  task?: boolean
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

/** One user's grant on a server: that user can reach it through the gateway. */
export interface GrantDTO {
  userId: string
  email?: string
  name?: string
  grantedAt: string
}

export interface GrantInput {
  userId: string
}

/**
 * A server's hooks: the text of each hook's rules, under the hook's property. The property is
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
  toolsCallPostTask?: string | null
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

/** GET /rules/hooks-schema: every hook, described for someone writing rules on it. */
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
  event?: 'progress' | 'elicit' | 'result' | 'task'
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

/** POST /rules/validate: what the engine's compiler and the sanity checks found. */
export interface RulesCheck {
  problems: RuleProblem[]
  warnings: RuleProblem[]
  /** Whether the text compiled to any rule; false when it has problems or holds only comments. */
  hasRules: boolean
}

/**
 * One place in a hook's tree: what a rule can read there, to any depth. A hook's tree has the item
 * (tool, prompt, resource, ...), the request, what came back and the server.
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

/** GET /portal: one server the caller holds. */
export interface PortalServerDTO {
  id: string
  slug: string
  name: string
  description: string
  mcpUrl: string
  url: string
  enabled: boolean
  selection: Selection
  /** "kind:name" the server exposes (seen on it and inside its selection). */
  capabilities: string[]
  /** Whether the server has rules on some hook. */
  hasRules: boolean
}

// The tasks extension (io.modelcontextprotocol/tasks).
export type TaskStatus = 'working' | 'input_required' | 'completed' | 'failed' | 'cancelled'

/** A question a task asks, keyed in inputRequests. */
export interface TaskInputRequest {
  method: string
  params?: { message?: string; mode?: string; requestedSchema?: JSONSchema; [key: string]: unknown }
}

/** A tools/call reply with the tasks opt-in (resultType "task" or an ordinary result), or a tasks/get reply. */
export interface TaskResult extends CallToolResult {
  resultType?: string
  taskId?: string
  status?: TaskStatus
  statusMessage?: string
  createdAt?: string
  lastUpdatedAt?: string
  ttlMs?: number
  pollIntervalMs?: number
  inputRequests?: Record<string, TaskInputRequest>
  /** On a reply that came back input_required: the state to send back with the answers. */
  requestState?: string
  result?: CallToolResult
  error?: { code?: number; message?: string; [key: string]: unknown }
}

// The skills extension (io.modelcontextprotocol/skills).
export interface MCPSkillFile {
  uri: string
  size?: number
  digest?: string
  mimeType?: string
}

export interface MCPSkill {
  uri: string
  frontmatter?: { name?: string; description?: string; [key: string]: unknown }
  /** The skill's files; a server may send a string such as "dynamic" instead. */
  resources?: MCPSkillFile[] | string
  [key: string]: unknown
}

export interface ListSkillsResult {
  skills?: MCPSkill[]
  nextCursor?: string
}
