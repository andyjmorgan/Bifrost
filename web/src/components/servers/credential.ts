import type { AuthKind, CredentialDTO, ServerDTO } from '../../api'
import type { Tone } from '../StatusPill'

export const AUTH_KINDS: { value: AuthKind; label: string; hint: string }[] = [
  { value: 'none', label: 'None', hint: 'The upstream accepts anonymous requests.' },
  { value: 'oauth', label: 'OAuth 2.1', hint: 'Discovery, client registration (CIMD or DCR) and consent are handled by the gateway.' },
  { value: 'basic', label: 'HTTP Basic', hint: 'A username and password sent on every request.' },
  { value: 'header', label: 'Header', hint: 'A static header such as X-Api-Key, stored encrypted.' },
]

export function authKindLabel(kind: AuthKind): string {
  return AUTH_KINDS.find((k) => k.value === kind)?.label ?? kind
}

export const authKindBadge: Record<AuthKind, 'muted' | 'plum' | 'info' | 'warning'> = {
  none: 'muted',
  oauth: 'plum',
  basic: 'info',
  header: 'warning',
}

/** The credential pill: state for authenticated kinds, "No auth" when the server has none. */
export function credentialStatus(server: Pick<ServerDTO, 'authKind' | 'credential'>): { tone: Tone; label: string } {
  if (server.authKind === 'none') return { tone: 'muted', label: 'No auth' }
  const state = server.credential?.state
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
