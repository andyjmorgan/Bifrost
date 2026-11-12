import { useCallback, useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react'
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
import { PoliciesPage } from './pages/Policies'
import { PolicyEditorPage } from './pages/PolicyEditor'
import { AuditPage } from './pages/Audit'
import { ProfilePage } from './pages/Profile'
import { MembersPage } from './pages/Members'
import { JoinPage } from './pages/Join'

function useAuthed(): boolean {
  return useSyncExternalStore(subscribeAuth, isAuthed, () => false)
}

// Gate: an unauthenticated visit bounces to the sign-in screen remembering where it was headed,
// both in router state and in sessionStorage (so /join/<token> survives the IdP round-trip).
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
  const refreshMe = useCallback(
    () =>
      api
        .me()
        .then((m) => {
          setMe(m)
          return m
        })
        .catch(() => null),
    [],
  )
  useEffect(() => {
    if (authed) void refreshMe()
  }, [authed, refreshMe])
  const value = useMemo(() => meValue(me, refreshMe), [me, refreshMe])
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
        <Route
          path="/join/:token"
          element={
            <RequireAuth>
              <JoinPage />
            </RequireAuth>
          }
        />
        <Route element={<AppShell />}>
          <Route path="/servers" element={<ServersPage />} />
          <Route path="/servers/:id" element={<ServerDetailPage />} />
          <Route path="/explorer" element={<ExplorerPage />} />
          <Route path="/policies" element={<PoliciesPage />} />
          <Route path="/policies/:id" element={<PolicyEditorPage />} />
          <Route path="/api-keys" element={<ApiKeysPage />} />
          <Route path="/audit" element={<AuditPage />} />
          <Route path="/members" element={<MembersPage />} />
          <Route path="/profile" element={<ProfilePage />} />
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
