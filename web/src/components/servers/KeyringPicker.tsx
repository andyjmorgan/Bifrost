import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { FormField } from '../FormField'
import { Callout } from '../Callout'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '../../ui/components/select'
import { errorMessage } from '../../api'
import { keyring, type OAuthAccount, type OAuthProvider, type Secret } from '../../api/keyring'
import { OAUTH_ACCOUNTS_ROUTE, SECRETS_ROUTE } from '../keyring/oauth'
import { pickOfAccount, pickOfSecret, secretHeaderSummary, secretNamesAHeader, type KeyringPick } from './credential'

const linkClass = 'underline decoration-accent/60 underline-offset-[3px] hover:text-accent'

/**
 * Picks the keyring credential a server authenticates with: one of the signed-in user's connected
 * OAuth accounts or one of their stored secrets. Only names are loaded and shown. The keyring
 * belongs to a person, so the list is always the caller's own, and saving makes them the owner of
 * the server's reference.
 *
 * `note` says what the server uses now when that is not in the caller's keyring (somebody else's,
 * or deleted since), which the list cannot show.
 */
export function KeyringPicker({
  value,
  onChange,
  disabled,
  note,
}: {
  value: KeyringPick
  onChange: (next: KeyringPick) => void
  disabled?: boolean
  note?: string
}) {
  const [accounts, setAccounts] = useState<OAuthAccount[]>([])
  const [secrets, setSecrets] = useState<Secret[]>([])
  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    let live = true
    // The provider list only names accounts; without it the slug is shown.
    Promise.all([keyring.oauth.accounts(), keyring.secrets.list(), keyring.providers.list().catch(() => [] as OAuthProvider[])])
      .then(([a, s, p]) => {
        if (!live) return
        // An account without a name cannot be referenced: the server needs provider and account.
        setAccounts((a ?? []).filter((x) => x.account))
        setSecrets((s ?? []).filter(secretNamesAHeader))
        setProviders(p ?? [])
      })
      .catch((e) => live && setError(errorMessage(e)))
      .finally(() => live && setLoaded(true))
    return () => {
      live = false
    }
  }, [])

  const providerName = (key: string) => providers.find((p) => p.key === key)?.name || key
  const empty = loaded && !error && accounts.length === 0 && secrets.length === 0
  const known = accounts.some((a) => pickOfAccount(a) === value) || secrets.some((s) => pickOfSecret(s) === value)

  return (
    <>
      <FormField
        label="Keyring credential"
        required
        hint="An OAuth account is sent as a bearer token, a secret in the header it describes; secrets that name no header are not listed. Everyone the server is granted to calls it with this credential."
      >
        <Select value={known ? value : ''} onValueChange={onChange} disabled={disabled || !loaded || empty}>
          <SelectTrigger aria-label="Keyring credential">
            <SelectValue placeholder={loaded ? (empty ? 'Nothing to choose from yet' : 'Choose an OAuth account or a secret') : 'Loading your keyring…'} />
          </SelectTrigger>
          <SelectContent>
            {accounts.length > 0 && (
              <SelectGroup>
                <SelectLabel>OAuth accounts</SelectLabel>
                {accounts.map((a) => (
                  <SelectItem key={a.id} value={pickOfAccount(a)}>
                    {providerName(a.provider)} · {a.account}
                  </SelectItem>
                ))}
              </SelectGroup>
            )}
            {secrets.length > 0 && (
              <SelectGroup>
                <SelectLabel>Secrets</SelectLabel>
                {secrets.map((s) => (
                  <SelectItem key={s.id} value={pickOfSecret(s)}>
                    {s.name}
                    <span className="ml-2 font-mono text-xs text-muted-foreground">{secretHeaderSummary(s)}</span>
                  </SelectItem>
                ))}
              </SelectGroup>
            )}
          </SelectContent>
        </Select>
      </FormField>

      {note && <Callout tone="info">{note}</Callout>}
      {error && <Callout tone="danger">Could not load your keyring: {error}</Callout>}
      {empty && (
        <Callout tone="info">
          Your keyring holds nothing a server can send. Connect an account under{' '}
          <Link className={linkClass} to={OAUTH_ACCOUNTS_ROUTE}>
            OAuth accounts
          </Link>
          , or store a header API key or an HTTP Basic login under{' '}
          <Link className={linkClass} to={SECRETS_ROUTE}>
            Secrets
          </Link>
          .
        </Callout>
      )}
    </>
  )
}
