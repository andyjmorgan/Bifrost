import { requestAt } from './client'
import type { components } from './keyring-schema'

// The keyring is Vault's REST API mounted in the gateway under /keyring, so every Vault path keeps
// its shape (/keyring/api/v1/api-keys, /keyring/api/v1/oauth/tokens, …). It is called with the same
// bearer token as the gateway's own API. The server only mounts it when the keyring is configured,
// and mounts nothing but /keyring/api/… and /keyring/healthz: page routes are never under /keyring.
//
// Naming: on the wire "api-keys" are stored secrets and "access-keys" are keys for calling the
// keyring. The gateway already has "API keys" of its own, so this client (and all UI text) says
// "secrets" for the former and "access keys" for the latter.

type S = components['schemas']

export type KeyringConfig = S['AppConfigResponse']
export type KeyringMe = S['MeResponse']

export type SecretKind = S['CredentialKind']
export type Secret = S['ApiKeyDto']
/** POST is an upsert by name. An empty `secret` keeps the stored value when the name exists. */
export type SaveSecretInput = Pick<S['CreateApiKeyRequest'], 'name' | 'kind'> & Partial<S['CreateApiKeyRequest']>
export type RevealedSecret = S['RevealApiKeyResponse']

export type OAuthAccount = S['OAuthTokenDto']
export type OAuthAccessToken = S['OAuthAccessTokenResponse']
export type OAuthAppConfig = S['OAuthConfigDto']
export type SaveOAuthAppConfigInput = Pick<S['UpsertOAuthConfigRequest'], 'provider' | 'clientId'> & Partial<S['UpsertOAuthConfigRequest']>

export type OAuthScope = S['OAuthScopeDto']
export type OAuthProvider = S['OAuthManifestDto']
export type TokenEndpointAuthMethod = NonNullable<OAuthProvider['tokenEndpointAuthMethod']>
/** A provider being added or edited: only the slug is certain until it is saved. */
export type OAuthProviderDraft = Pick<OAuthProvider, 'key'> & Partial<Omit<OAuthProvider, 'key'>>
/** What OIDC discovery fills in; the server answers with a partial manifest. */
export type DiscoveredProvider = Partial<OAuthProvider>

export type AccessKey = S['AccessKeyDto']
export type AccessKeyScope = 'vault:read' | 'vault:readwrite' | 'vault:audit'
export type CreateAccessKeyInput = Pick<S['CreateAccessKeyRequest'], 'name'> & { description?: string; scopes: AccessKeyScope[] }
export type CreatedAccessKey = S['CreatedAccessKeyResponse']

/**
 * `requestId` is added by hand (the schema file predates it): the gateway's request id, on a row
 * written for a request the gateway forwarded to an MCP server. The gateway's trail has it too.
 */
export type KeyringAuditEvent = S['AuditEventDto'] & { requestId?: string | null }
/** The generated page types its counters as `unknown` (int32 without a type), so narrow them here. */
export interface KeyringAuditPage {
  items: KeyringAuditEvent[]
  total: number
  limit: number
  offset: number
}
export interface KeyringAuditQuery {
  limit?: number
  offset?: number
  type?: string
  outcome?: string
  userId?: string
  requestId?: string
  since?: string
  until?: string
}

export const KEYRING_PREFIX = '/keyring'
const BASE = `${KEYRING_PREFIX}/api/v1`

const get = <T>(path: string) => requestAt<T>(BASE, path, {}, true)
const send = <T>(method: string, path: string, body?: unknown) =>
  requestAt<T>(BASE, path, { method, body: body === undefined ? undefined : JSON.stringify(body) }, true)
const enc = encodeURIComponent

function qs(params: Record<string, string | number | undefined>): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') p.set(k, String(v))
  }
  const s = p.toString()
  return s ? `?${s}` : ''
}

/**
 * Ask the server whether the keyring is mounted. GET /keyring/api/config is unauthenticated and
 * answers 404 when the keyring is off. Anything that is not a JSON 200 counts as off too: without
 * the mount the request can fall through to the SPA and come back as index.html.
 */
export async function probeKeyring(): Promise<KeyringConfig | null> {
  try {
    const res = await fetch(`${KEYRING_PREFIX}/api/config`, { headers: { Accept: 'application/json' } })
    if (!res.ok) return null
    if (!(res.headers.get('Content-Type') ?? '').includes('json')) return null
    const body: unknown = await res.json()
    return body && typeof body === 'object' ? (body as KeyringConfig) : null
  } catch {
    return null
  }
}

export const keyring = {
  /** The caller as the keyring sees them: its own user id and tenant. */
  me: () => get<KeyringMe>('/me'),

  /** Stored secrets ("api-keys" on the wire). */
  secrets: {
    list: () => get<Secret[]>('/api-keys'),
    save: (input: SaveSecretInput) => send<S['CreatedApiKeyResponse']>('POST', '/api-keys', input),
    remove: (id: string) => send<void>('DELETE', `/api-keys/${enc(id)}`),
    /** Decrypts on demand; audited by the server as a read. */
    reveal: (name: string) => get<RevealedSecret>(`/api-keys/${enc(name)}/reveal`),
  },

  /** Connected OAuth accounts and the app credentials each provider connects with. */
  oauth: {
    accounts: () => get<OAuthAccount[]>('/oauth/tokens'),
    disconnect: (id: string) => send<void>('DELETE', `/oauth/tokens/${enc(id)}`),
    /** A fresh access token for the account; the server refreshes it first when it has expired. */
    accessToken: (provider: string, account?: string) => get<OAuthAccessToken>(`/oauth/${enc(provider)}/token${qs({ account })}`),
    appConfigs: () => get<OAuthAppConfig[]>('/oauth/configs'),
    saveAppConfig: (input: SaveOAuthAppConfigInput) => send<S['OAuthConfigCreatedResponse']>('POST', '/oauth/configs', input),
    removeAppConfig: (id: string) => send<void>('DELETE', `/oauth/configs/${enc(id)}`),
    /** Begins the authorization-code flow; send the browser to the returned URL. */
    connect: (provider: string, scopes?: string[]) => get<S['ConnectResponse']>(`/oauth/${enc(provider)}/connect${qs({ scopes: scopes?.join(' ') })}`),
  },

  /** OAuth provider manifests: the ones added here and the built-in template library. */
  providers: {
    list: () => get<OAuthProvider[]>('/manifests'),
    templates: () => get<OAuthProvider[]>('/manifests/templates'),
    save: (input: OAuthProviderDraft) => send<S['KeyResponse']>('POST', '/manifests/oauth', input),
    /** Reads an issuer's OIDC discovery document and returns the endpoints and scopes it names. */
    discover: (url: string) => send<DiscoveredProvider>('POST', '/manifests/oauth/discover', { url }),
    remove: (key: string) => send<void>('DELETE', `/manifests/oauth/${enc(key)}`),
  },

  /** Keys for calling the keyring itself (dwv_…). The secret is returned once, at creation. */
  accessKeys: {
    list: () => get<AccessKey[]>('/access-keys'),
    create: (input: CreateAccessKeyInput) => send<CreatedAccessKey>('POST', '/access-keys', input),
    setEnabled: (id: string, enabled: boolean) => send<S['AccessKeyEnabledResponse']>('PATCH', `/access-keys/${enc(id)}`, { enabled }),
    remove: (id: string) => send<void>('DELETE', `/access-keys/${enc(id)}`),
  },

  audit: {
    list: (q: KeyringAuditQuery = {}) => get<KeyringAuditPage>(`/audit${qs(q as Record<string, string | number | undefined>)}`),
  },
}
