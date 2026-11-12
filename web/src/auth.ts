import { UserManager, WebStorageStateStore, type User } from 'oidc-client-ts'
import type { ConfigDTO } from './api/types'

// Auth bootstrap. GET /api/v1/config is unauthenticated and decides the mode:
//  - devMode: no login at all, requests carry no Authorization header.
//  - otherwise: Keycloak OIDC Authorization Code + PKCE via oidc-client-ts, tokens in
//    sessionStorage, bearer on every /api/v1 call.
// "/" is the sign-in screen when there is no session and the app when there is. We remember the
// route the visitor wanted and return there after the IdP round-trip through /auth/callback.

let config: ConfigDTO | null = null
let mgr: UserManager | null = null
let current: User | null = null

const RETURN_TO_KEY = 'mcpgw.returnTo'
export const DEFAULT_ROUTE = '/servers'

const listeners = new Set<() => void>()
function notify() {
  listeners.forEach((l) => l())
}

export function subscribeAuth(cb: () => void): () => void {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

export function getConfig(): ConfigDTO | null {
  return config
}

export function isDevMode(): boolean {
  return !!config?.devMode
}

export async function initAuth(): Promise<void> {
  const res = await fetch('/api/v1/config', { headers: { Accept: 'application/json' } })
  if (!res.ok) throw new Error(`config: ${res.status} ${res.statusText}`)
  config = (await res.json()) as ConfigDTO
  if (config.devMode) return
  if (!config.oidcIssuer || !config.oidcClientId) throw new Error('config: OIDC issuer or client id missing')

  mgr = new UserManager({
    authority: config.oidcIssuer,
    client_id: config.oidcClientId,
    redirect_uri: `${window.location.origin}/auth/callback`,
    post_logout_redirect_uri: `${window.location.origin}/`,
    response_type: 'code',
    scope: 'openid profile email',
    automaticSilentRenew: true,
    loadUserInfo: false,
    userStore: new WebStorageStateStore({ store: window.sessionStorage }),
    stateStore: new WebStorageStateStore({ store: window.sessionStorage }),
  })
  mgr.events.addUserLoaded((u) => {
    current = u
    notify()
  })
  mgr.events.addUserUnloaded(() => {
    current = null
    notify()
  })
  mgr.events.addSilentRenewError(() => {
    current = null
    notify()
  })

  // The callback route completes the sign-in; everywhere else we just read the stored user.
  if (window.location.pathname !== '/auth/callback') {
    current = await mgr.getUser()
  }
}

/** True when auth is off (dev) or there is a live session. */
export function isAuthed(): boolean {
  if (!config) return false
  if (config.devMode) return true
  return !!current && !current.expired
}

/**
 * Remember where an unauthenticated visitor was headed (e.g. /join/<token>) so the sign-in
 * round-trip lands back there even if the router state is lost on a reload of "/".
 */
export function rememberReturnTo(path: string): void {
  if (!path.startsWith('/') || path.startsWith('/auth') || path === '/') return
  try {
    sessionStorage.setItem(RETURN_TO_KEY, path)
  } catch {
    // storage unavailable: the router state still carries it for this navigation
  }
}

/** Begin the redirect to the IdP. `idpHint` is a Keycloak identity-provider alias. */
export function login(returnTo?: string, idpHint?: string): void {
  if (!mgr) {
    window.location.assign(returnTo && returnTo.startsWith('/') ? returnTo : DEFAULT_ROUTE)
    return
  }
  // An explicit returnTo wins; otherwise keep whatever a 401 bounce stored, else the default.
  const safe = returnTo && returnTo.startsWith('/') && !returnTo.startsWith('/auth') && returnTo !== '/' ? returnTo : undefined
  try {
    if (safe) sessionStorage.setItem(RETURN_TO_KEY, safe)
    else if (!sessionStorage.getItem(RETURN_TO_KEY)) sessionStorage.setItem(RETURN_TO_KEY, DEFAULT_ROUTE)
  } catch {
    // storage unavailable: we land on the default route
  }
  void mgr.signinRedirect(idpHint ? { extraQueryParams: { kc_idp_hint: idpHint } } : undefined)
}

/** Finish the code exchange on /auth/callback. Returns the route to land on. */
export async function completeLogin(): Promise<string> {
  if (!mgr) return DEFAULT_ROUTE
  current = await mgr.signinRedirectCallback()
  notify()
  let to = DEFAULT_ROUTE
  try {
    to = sessionStorage.getItem(RETURN_TO_KEY) || DEFAULT_ROUTE
    sessionStorage.removeItem(RETURN_TO_KEY)
  } catch {
    // ignore
  }
  return to
}

export async function getAccessToken(): Promise<string> {
  if (!mgr) return ''
  let u = await mgr.getUser()
  if (!u || u.expired) {
    try {
      u = await mgr.signinSilent()
      current = u
    } catch {
      u = null
    }
  }
  return u?.access_token ?? ''
}

/** Drop the local session and return to the sign-in screen (no IdP round-trip). */
export async function clearSession(): Promise<void> {
  current = null
  try {
    await mgr?.removeUser()
  } catch {
    // ignore
  }
  notify()
}

let signingOut = false
/** A 401 from the API: forget the session and go back to "/", remembering where we were. */
export function signOutOn401(): void {
  if (signingOut) return
  signingOut = true
  try {
    sessionStorage.setItem(RETURN_TO_KEY, window.location.pathname + window.location.search)
  } catch {
    // ignore
  }
  void clearSession().finally(() => {
    window.location.assign('/')
  })
}

export function logout(): void {
  if (mgr) {
    void mgr.signoutRedirect().catch(() => clearSession().then(() => window.location.assign('/')))
  } else {
    window.location.assign('/')
  }
}

export function profile() {
  return current?.profile
}
