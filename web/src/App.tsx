import { useEffect, useState, useSyncExternalStore } from 'react'
import { Navigate, Outlet, Route, Routes, useLocation } from 'react-router-dom'
import { Toaster } from 'sonner'
import { AppLayout } from './components/AppLayout'
import { DEFAULT_ROUTE, isAuthed, subscribeAuth } from './auth'
import { useTheme } from './theme'
import { api, type MeDTO } from './api'
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

function useAuthed(): boolean {
  return useSyncExternalStore(subscribeAuth, isAuthed, () => false)
}

// The signed-in shell: gated, loads the caller, and renders the app chrome around the active route.
// An unauthenticated visit bounces to the sign-in screen remembering where it was headed.
function AppShell() {
  const [me, setMe] = useState<MeDTO | null>(null)
  const location = useLocation()
  const authed = useAuthed()
  useEffect(() => {
    if (authed) api.me().then(setMe).catch(() => {})
  }, [authed])
  if (!authed) return <Navigate to="/" replace state={{ from: location.pathname + location.search }} />
  return (
    <AppLayout me={me}>
      <Outlet context={{ me }} />
    </AppLayout>
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
        <Route element={<AppShell />}>
          <Route path="/servers" element={<ServersPage />} />
          <Route path="/servers/:id" element={<ServerDetailPage />} />
          <Route path="/explorer" element={<ExplorerPage />} />
          <Route path="/policies" element={<PoliciesPage />} />
          <Route path="/policies/:id" element={<PolicyEditorPage />} />
          <Route path="/api-keys" element={<ApiKeysPage />} />
          <Route path="/audit" element={<AuditPage />} />
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
