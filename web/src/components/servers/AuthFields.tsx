import type { AuthInput, AuthKind } from '../../api'
import { FormField } from '../FormField'
import { Input } from '../../ui/components/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/components/select'
import { keyringEnabled } from '../../keyring'
import { AUTH_KINDS, KEYRING_CHOICE, authInputOfPick, isKeyringKind, type KeyringPick } from './credential'
import { KeyringPicker } from './KeyringPicker'

/** Draft state for the auth section; `scopes` is the space-separated override text. */
export interface AuthDraft {
  kind: AuthKind
  username: string
  password: string
  headerName: string
  headerValue: string
  scopes: string
  /** What is picked from the keyring, for the keyring kinds; '' until something is. */
  keyring: KeyringPick
}

export const emptyAuthDraft = (kind: AuthKind = 'none'): AuthDraft => ({
  kind,
  username: '',
  password: '',
  headerName: 'X-Api-Key',
  headerValue: '',
  scopes: '',
  keyring: '',
})

export function toAuthInput(d: AuthDraft): AuthInput {
  switch (d.kind) {
    case 'basic':
      return { kind: 'basic', username: d.username.trim(), password: d.password }
    case 'header':
      return { kind: 'header', headerName: d.headerName.trim() || 'X-Api-Key', headerValue: d.headerValue }
    case 'oauth': {
      const scopes = d.scopes.split(/\s+/).map((s) => s.trim()).filter(Boolean)
      return scopes.length ? { kind: 'oauth', scopes } : { kind: 'oauth' }
    }
    case 'keyring_oauth':
    case 'keyring_secret':
      // validateAuth has refused an empty pick before this is called.
      return authInputOfPick(d.keyring) ?? { kind: 'none' }
    default:
      return { kind: 'none' }
  }
}

export function validateAuth(d: AuthDraft): string | undefined {
  if (d.kind === 'basic' && (!d.username.trim() || !d.password)) return 'Username and password are required for HTTP Basic.'
  if (d.kind === 'header' && (!d.headerName.trim() || !d.headerValue)) return 'Header name and value are required.'
  if (isKeyringKind(d.kind) && !authInputOfPick(d.keyring)) return 'Choose an OAuth account or a secret from your keyring.'
  return undefined
}

/**
 * The auth kind select plus its conditional fields; shared by create and replace-credential.
 *
 * "From the keyring" is one choice for both keyring kinds, offered when the keyring is enabled (or
 * the server already uses it, so the dialog can show what it has). `keyringNote` is passed through
 * to the picker.
 */
export function AuthFields({
  value,
  onChange,
  disabled,
  keyringNote,
}: {
  value: AuthDraft
  onChange: (next: AuthDraft) => void
  disabled?: boolean
  keyringNote?: string
}) {
  const set = <K extends keyof AuthDraft>(key: K, v: AuthDraft[K]) => onChange({ ...value, [key]: v })
  const fromKeyring = isKeyringKind(value.kind)
  const choices: { value: string; label: string; hint: string }[] = keyringEnabled() || fromKeyring ? [...AUTH_KINDS, KEYRING_CHOICE] : AUTH_KINDS
  const selected = fromKeyring ? KEYRING_CHOICE.value : value.kind
  const choose = (v: string) => {
    if (v !== KEYRING_CHOICE.value) return set('kind', v as AuthKind)
    // Which keyring kind it is follows from the pick; until then either stands for "keyring".
    if (!fromKeyring) onChange({ ...value, kind: authInputOfPick(value.keyring)?.kind ?? 'keyring_secret' })
  }
  const pick = (next: KeyringPick) => onChange({ ...value, keyring: next, kind: authInputOfPick(next)?.kind ?? value.kind })

  return (
    <>
      <FormField label="Authentication" required hint={choices.find((k) => k.value === selected)?.hint}>
        <Select value={selected} onValueChange={choose} disabled={disabled}>
          <SelectTrigger aria-label="Authentication kind">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {choices.map((k) => (
              <SelectItem key={k.value} value={k.value}>
                {k.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </FormField>

      {value.kind === 'basic' && (
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField label="Username" required>
            <Input value={value.username} onChange={(e) => set('username', e.target.value)} autoComplete="off" disabled={disabled} />
          </FormField>
          <FormField label="Password" required>
            <Input
              type="password"
              value={value.password}
              onChange={(e) => set('password', e.target.value)}
              autoComplete="new-password"
              className="font-mono"
              disabled={disabled}
            />
          </FormField>
        </div>
      )}

      {value.kind === 'header' && (
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField label="Header name" required>
            <Input value={value.headerName} onChange={(e) => set('headerName', e.target.value)} className="font-mono" disabled={disabled} />
          </FormField>
          <FormField label="Header value" required>
            <Input
              type="password"
              value={value.headerValue}
              onChange={(e) => set('headerValue', e.target.value)}
              autoComplete="new-password"
              className="font-mono"
              disabled={disabled}
            />
          </FormField>
        </div>
      )}

      {value.kind === 'oauth' && (
        <FormField
          label="Scopes override"
          hint="Optional, space-separated. Leave empty to use what the server advertises. The gateway discovers the authorization server and registers itself (CIMD, falling back to DCR); you will be sent to the provider to consent."
        >
          <Input
            value={value.scopes}
            onChange={(e) => set('scopes', e.target.value)}
            placeholder="read:tools write:files"
            className="font-mono"
            disabled={disabled}
          />
        </FormField>
      )}

      {fromKeyring && <KeyringPicker value={value.keyring} onChange={pick} disabled={disabled} note={keyringNote} />}
    </>
  )
}
