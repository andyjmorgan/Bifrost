import { probeKeyring, type KeyringConfig } from './api/keyring'

// Whether this gateway has a keyring. The server only mounts it when it is configured, so the SPA
// asks once at startup (alongside the auth bootstrap) and every keyring surface (the sidebar group,
// its routes, the second tab on API keys and Audit) keys off the answer. Probing before the first
// render keeps the navigation from appearing or vanishing after paint.

let config: KeyringConfig | null = null

/** Never rejects: a keyring that cannot be reached is simply off. */
export async function initKeyring(): Promise<void> {
  config = await probeKeyring()
}

export function keyringEnabled(): boolean {
  return config !== null
}
