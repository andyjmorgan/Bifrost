import { useOutletContext } from 'react-router-dom'
import { PageHeader } from '../components/PageHeader'
import { DefinitionList, Definition } from '../components/DefinitionList'
import { Badge } from '../ui/components/badge'
import { getConfig } from '../auth'
import type { MeDTO } from '../api'

/** Identity only; anything manageable lives under its own nav entry. */
export function ProfilePage() {
  const { me } = useOutletContext<{ me: MeDTO | null }>()
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
        <Definition label="Owner" mono>
          {me?.ownerId || '—'}
        </Definition>
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
