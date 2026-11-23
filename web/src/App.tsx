import { useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react'
import { Navigate, Outlet, Route, Routes, useLocation } from 'react-router-dom'
import { Toaster } from 'sonner'
import { AppLayout } from './components/AppLayout'
import { DEFAULT_ROUTE, isAuthed, rememberReturnTo, subscribeAuth } from './auth'
import { useTheme } from './theme'
import { api, type MeDTO } from './api'
import { MeContext, meValue } from './me'
import { LoginPage } from './pages/Login'
import { AuthCallbackPage } from './pages/AuthCallback'
import { ServersPage } from './pages/Servers'
import { ServerDetailPage } from './pages/ServerDetail'
import { ExplorerPage } from './pages/Explorer'
import { ApiKeysPage } from './pages/ApiKeys'
import { PortalPage } from './pages/Portal'
import { AuditPage } from './pages/Audit'
import { ProfilePage } from './pages/Profile'
import { SecretsPage } from './pages/Secrets'
import { ConnectPage } from './pages/Connect'
import { ProvidersPage } from './pages/Providers'
import { keyringEnabled } from './keyring'

function useAuthed(): boolean {
  return useSyncExternalStore(subscribeAuth, isAuthed, () => false)
}

// Gate: an unauthenticated visit bounces to the sign-in screen remembering where it was headed,
// both in router state and in sessionStorage (so a deep link survives the IdP round-trip).
function RequireAuth({ children }: { children: ReactNode }) {
  const location = useLocation()
  const authed = useAuthed()
  if (!authed) {
    const from = location.pathname + location.search
    rememberReturnTo(from)
    return <Navigate to="/" replace state={{ from }} />
  }
  return <>{children}</>
}

// The signed-in shell: loads the caller and renders the app chrome around the active route.
function AppShell() {
  const [me, setMe] = useState<MeDTO | null>(null)
  const authed = useAuthed()
  useEffect(() => {
    if (!authed) return
    api
      .me()
      .then(setMe)
      .catch(() => setMe(null))
  }, [authed])
  const value = useMemo(() => meValue(me), [me])
  return (
    <RequireAuth>
      <MeContext.Provider value={value}>
        <AppLayout>
          <Outlet />
        </AppLayout>
      </MeContext.Provider>
    </RequireAuth>
  )
}

/** An old address for a page that moved: go to the new one, keeping the query string. */
function Moved({ to }: { to: string }) {
  const { search } = useLocation()
  return <Navigate to={to + search} replace />
}

// "/" is the sign-in screen until there's a session, then the app.
function Root() {
  const authed = useAuthed()
  return authed ? <Navigate to={DEFAULT_ROUTE} replace /> : <LoginPage />
}

export function App() {
  const theme = useTheme()
  return (
    <>
      <Routes>
        <Route path="/" element={<Root />} />
        <Route path="/auth/callback" element={<AuthCallbackPage />} />
        <Route element={<AppShell />}>
          <Route path="/portal" element={<PortalPage />} />
          <Route path="/servers" element={<ServersPage />} />
          <Route path="/servers/:id" element={<ServerDetailPage />} />
          <Route path="/explorer" element={<ExplorerPage />} />
          <Route path="/api-keys" element={<ApiKeysPage />} />
          <Route path="/audit" element={<AuditPage />} />
          <Route path="/profile" element={<ProfilePage />} />
          {/* The keyring's pages exist only when the server has a keyring; otherwise these
              addresses fall through to the catch-all like any unknown route. */}
          {keyringEnabled() && (
            <>
              <Route path="/secrets" element={<SecretsPage />} />
              <Route path="/oauth-accounts" element={<SecretsPage />} />
              <Route path="/connect" element={<ConnectPage />} />
              <Route path="/providers" element={<ProvidersPage />} />
              {/* The keyring's OAuth callback answers with /connect?connected=… or ?oauth_error=…;
                  left unconfigured it falls back to Vault's old /oauthconnect, so honour that too.
                  (Vault's other page addresses are redirected by the server, on the Vault host.) */}
              <Route path="/oauthconnect" element={<Moved to="/connect" />} />
            </>
          )}
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      <Toaster
        theme={theme}
        position="bottom-right"
        toastOptions={{
          classNames: {
            toast: '!rounded-xl !border !border-border !bg-card !text-foreground !shadow-2xl !font-sans',
            description: '!text-muted-foreground',
            success: '[&_svg]:!text-success',
            error: '[&_svg]:!text-danger',
          },
        }}
      />
    </>
  )
}
