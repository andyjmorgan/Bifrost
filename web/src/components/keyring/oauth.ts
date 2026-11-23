import { useEffect } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

export const SECRETS_ROUTE = '/secrets'
export const OAUTH_ACCOUNTS_ROUTE = '/oauth-accounts'
export const CONNECT_ROUTE = '/connect'
export const PROVIDERS_ROUTE = '/providers'

/**
 * OAuth scopes can be whole URLs (https://www.googleapis.com/auth/calendar) that overflow a tag;
 * show the meaningful tail. Short scopes (gist, Calendars.Read) are shown as they are.
 */
export const shortScope = (s: string) => (s.includes('://') ? s.replace(/\/+$/, '').split('/').pop() || s : s)

/**
 * The end of a connect flow. The provider redirects to the server's callback, which sends the
 * browser to /connect with the outcome in the query string, the same two parameters the Vault
 * console read: `connected=<provider>` on success, `oauth_error=<reason>` on failure. Report it in
 * a toast and settle on the OAuth accounts page with the parameters gone, so a reload does not
 * repeat the toast. Connect is where the callback lands; the accounts page runs this too, so an
 * outcome that arrives there is not lost.
 */
export function useOAuthOutcome(): void {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  useEffect(() => {
    const connected = params.get('connected')
    const error = params.get('oauth_error')
    if (!connected && !error) return
    // A fixed id so the toast is not doubled when the effect runs twice (StrictMode) or the
    // callback lands on one keyring page and is forwarded to another.
    if (connected) toast.success(`Connected ${connected}`, { id: 'keyring-oauth-outcome', description: 'The account is ready for agents to use.' })
    else toast.error('The account was not connected', { id: 'keyring-oauth-outcome', description: error ?? undefined })
    navigate(OAUTH_ACCOUNTS_ROUTE, { replace: true })
  }, [params, navigate])
}
