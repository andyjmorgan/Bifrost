import { PageHeader } from '../components/PageHeader'
import { DefinitionList, Definition } from '../components/DefinitionList'
import { Badge } from '../ui/components/badge'
import { getConfig } from '../auth'
import { useMe } from '../me'

/** Identity only; anything manageable lives under its own nav entry. */
export function ProfilePage() {
  const { me } = useMe()
  const cfg = getConfig()
  return (
    <div className="space-y-6">
      <PageHeader eyebrow="Account" title="Profile" description="Who the gateway thinks you are, and what this session may do." />
      <DefinitionList>
        <Definition label="Name">{me?.name || '—'}</Definition>
        <Definition label="Email">{me?.email || '—'}</Definition>
        <Definition label="Subject" mono>
          {me?.subject || '—'}
        </Definition>
        <Definition label="User ID" mono>
          {me?.userId || '—'}
        </Definition>
        <Definition label="Organisation">
          {me ? (
            <span className="inline-flex flex-wrap items-center gap-2">
              <span>{me.orgName}</span>
              <Badge variant={me.personalOrg ? 'muted' : 'info'}>{me.personalOrg ? 'personal' : 'shared'}</Badge>
            </span>
          ) : (
            '—'
          )}
        </Definition>
        <Definition label="Role">{me?.role ? <Badge variant={me.role === 'admin' ? 'accent' : 'muted'}>{me.role}</Badge> : '—'}</Definition>
        <Definition label="Session type">{me?.type || '—'}</Definition>
        <Definition label="Scopes">
          <div className="flex flex-wrap gap-1">
            {me?.scopes?.length ? me.scopes.map((s) => <Badge key={s} mono variant="accent">{s}</Badge>) : '—'}
          </div>
        </Definition>
        <Definition label="Gateway" mono>
          {cfg ? `${cfg.publicBaseUrl} · v${cfg.version}${cfg.devMode ? ' · dev mode' : ''}` : '—'}
        </Definition>
      </DefinitionList>
    </div>
  )
}
