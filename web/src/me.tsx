import { createContext, useContext } from 'react'
import type { MeDTO } from './api'

// The caller's identity in the active org, loaded once by the signed-in shell and refetched
// after anything that changes the active org (join, leave, switch, removing yourself).

export interface MeContextValue {
  me: MeDTO | null
  /** True only once /me has loaded and the caller is an admin of the active org. */
  isAdmin: boolean
  role?: MeDTO['role']
  orgName?: string
  personalOrg?: boolean
  userId?: string
  refreshMe: () => Promise<MeDTO | null>
}

export const MeContext = createContext<MeContextValue>({
  me: null,
  isAdmin: false,
  refreshMe: async () => null,
})

export function meValue(me: MeDTO | null, refreshMe: () => Promise<MeDTO | null>): MeContextValue {
  return {
    me,
    isAdmin: me?.role === 'admin',
    role: me?.role,
    orgName: me?.orgName,
    personalOrg: me?.personalOrg,
    userId: me?.userId,
    refreshMe,
  }
}

export function useMe(): MeContextValue {
  return useContext(MeContext)
}
