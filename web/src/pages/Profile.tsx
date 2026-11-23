import { useEffect, useState } from 'react'
import { PageHeader } from '../components/PageHeader'
import { DefinitionList, Definition } from '../components/DefinitionList'
import { Badge } from '../ui/components/badge'
import { getConfig } from '../auth'
import { useMe } from '../me'
import { keyringEnabled } from '../keyring'
import { keyring, type KeyringMe } from '../api/keyring'

/** Identity only; anything manageable lives under its own nav entry. */
export function ProfilePage() {
  const { me } = useMe()
  const cfg = getConfig()
  // The keyring keeps its own user and tenant ids on every row it stores; show them when it is on.
  const [keyringMe, setKeyringMe] = useState<KeyringMe>()
  useEffect(() => {
    if (!keyringEnabled()) return
    keyring
      .me()
      .then(setKeyringMe)
      .catch(() => setKeyringMe(undefined))
  }, [])
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
        <Definition label="Session type">{me?.type || '—'}</Definition>
        <Definition label="Scopes">
          <div className="flex flex-wrap gap-1">
            {me?.scopes?.length ? me.scopes.map((s) => <Badge key={s} mono variant="accent">{s}</Badge>) : '—'}
          </div>
        </Definition>
        <Definition label="Gateway" mono>
          {cfg ? `${cfg.publicBaseUrl} · v${cfg.version}${cfg.devMode ? ' · dev mode' : ''}` : '—'}
        </Definition>
        {keyringMe && (
          <>
            <Definition label="Keyring user" mono>
              {keyringMe.userId || '—'}
            </Definition>
            <Definition label="Keyring tenant" mono>
              {keyringMe.tenantId || 'default'}
            </Definition>
          </>
        )}
      </DefinitionList>
    </div>
  )
}
