import type { AuthInput, AuthKind, CredentialDTO, ServerDTO, UserDTO } from '../../api'
import type { OAuthAccount, Secret } from '../../api/keyring'
import type { Tone } from '../StatusPill'

/** The kinds whose secret the gateway stores itself. */
export const AUTH_KINDS: { value: AuthKind; label: string; hint: string }[] = [
  { value: 'none', label: 'None', hint: 'The upstream accepts anonymous requests.' },
  { value: 'oauth', label: 'OAuth 2.1', hint: 'Discovery, client registration (CIMD or DCR) and consent are handled by the gateway.' },
  { value: 'basic', label: 'HTTP Basic', hint: 'A username and password sent on every request.' },
  { value: 'header', label: 'Header', hint: 'A static header such as X-Api-Key, stored encrypted.' },
]

/**
 * The fifth choice in the authentication select, shown only when the keyring is enabled. It stands
 * for both keyring kinds: which one is decided by what is picked from the keyring.
 */
export const KEYRING_CHOICE = {
  value: 'keyring',
  label: 'From the keyring',
  hint: 'An OAuth account or a secret from your keyring. The gateway stores its name and fetches the value for each request.',
} as const

const KEYRING_LABELS: Partial<Record<AuthKind, string>> = {
  keyring_oauth: 'Keyring OAuth account',
  keyring_secret: 'Keyring secret',
}

export function isKeyringKind(kind: AuthKind): kind is 'keyring_oauth' | 'keyring_secret' {
  return kind === 'keyring_oauth' || kind === 'keyring_secret'
}

export function authKindLabel(kind: AuthKind): string {
  return AUTH_KINDS.find((k) => k.value === kind)?.label ?? KEYRING_LABELS[kind] ?? kind
}

/** The kind as the server list's narrow mono tag shows it: both keyring kinds read "keyring". */
export function authKindTag(kind: AuthKind): string {
  return isKeyringKind(kind) ? 'keyring' : kind
}

export const authKindBadge: Record<AuthKind, 'muted' | 'plum' | 'info' | 'warning' | 'accent'> = {
  none: 'muted',
  oauth: 'plum',
  basic: 'info',
  header: 'warning',
  keyring_oauth: 'accent',
  keyring_secret: 'accent',
}

/** The credential pill: state for authenticated kinds, "No auth" when the server has none. */
export function credentialStatus(server: Pick<ServerDTO, 'authKind' | 'credential'>): { tone: Tone; label: string } {
  if (server.authKind === 'none') return { tone: 'muted', label: 'No auth' }
  const state = server.credential?.state
  // A keyring credential is a reference: whether it resolves is only known when it is used.
  if (state === 'ok' && isKeyringKind(server.authKind)) return { tone: 'success', label: 'Linked' }
  switch (state) {
    case 'ok':
      return { tone: 'success', label: 'Connected' }
    case 'pending':
      return { tone: 'warning', label: 'Awaiting consent' }
    case 'broken':
      return { tone: 'danger', label: 'Broken' }
    default:
      return { tone: 'muted', label: 'No credential' }
  }
}

export function isOAuth(c?: CredentialDTO | null): boolean {
  return c?.kind === 'oauth'
}

// ---- keyring references ------------------------------------------------------------------------

/**
 * A keyring credential as the picker's select value. Provider slugs, account names and secret
 * names are free text, so the parts are joined by a newline, which none of them can hold.
 */
export type KeyringPick = string

export const pickOfAccount = (a: Pick<OAuthAccount, 'provider' | 'account'>): KeyringPick => ['oauth', a.provider, a.account].join('\n')
export const pickOfSecret = (s: Pick<Secret, 'name'>): KeyringPick => ['secret', s.name].join('\n')

/** The pick for the keyring credential a server already uses, or '' when it uses none. */
export function pickOfCredential(c?: CredentialDTO | null): KeyringPick {
  if (c?.kind === 'keyring_oauth' && c.keyringProvider && c.keyringAccount) return pickOfAccount({ provider: c.keyringProvider, account: c.keyringAccount })
  if (c?.kind === 'keyring_secret' && c.keyringSecretName) return pickOfSecret({ name: c.keyringSecretName })
  return ''
}

/** The credential part of a request for a pick, or undefined when nothing is picked. */
export function authInputOfPick(pick: KeyringPick): AuthInput | undefined {
  const [kind, a, b] = pick.split('\n')
  if (kind === 'oauth' && a && b) return { kind: 'keyring_oauth', keyringProvider: a, keyringAccount: b }
  if (kind === 'secret' && a) return { kind: 'keyring_secret', keyringSecretName: a }
  return undefined
}

/**
 * Whether a stored secret can authenticate an HTTP request. The server applies the same rule: HTTP
 * Basic needs its username, and anything else must name a header (a header API key defaults to
 * Authorization). An SSH login or a connection string describes no header, so it is not offered.
 */
export function secretNamesAHeader(s: Pick<Secret, 'kind' | 'header' | 'username'>): boolean {
  if (s.kind === 'http_basic') return !!s.username
  return s.kind === 'header_api_key' || !!s.header
}

/** How a usable secret is sent, for the picker: "Authorization: Basic" or "X-Api-Key: Key …". */
export function secretHeaderSummary(s: Pick<Secret, 'kind' | 'header' | 'prefix'>): string {
  if (s.kind === 'http_basic') return `${s.header || 'Authorization'}: Basic`
  return `${s.header || 'Authorization'}${s.prefix?.trim() ? `: ${s.prefix.trim()} …` : ''}`
}

/** The name of the keyring credential a server uses: "x · alice" or the secret's name. Never a value. */
export function keyringCredentialName(c?: CredentialDTO | null): string {
  if (c?.kind === 'keyring_oauth') return [c.keyringProvider, c.keyringAccount].filter(Boolean).join(' · ')
  if (c?.kind === 'keyring_secret') return c.keyringSecretName ?? ''
  return ''
}

/** Whose keyring a credential is in, as a phrase: "your keyring" or "Dana's keyring". */
export function keyringOwnerPhrase(c: CredentialDTO | null | undefined, meId: string | undefined, users: UserDTO[]): string {
  const owner = c?.keyringOwner
  if (!owner) return 'the keyring'
  if (owner === meId) return 'your keyring'
  const u = users.find((x) => x.id === owner)
  const name = u?.name || u?.email
  return name ? `${name}'s keyring` : "another user's keyring"
}
