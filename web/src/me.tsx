import { createContext, useContext } from 'react'
import type { MeDTO } from './api'

// The caller's identity, loaded once by the signed-in shell.

export interface MeContextValue {
  me: MeDTO | null
  userId?: string
}

export const MeContext = createContext<MeContextValue>({ me: null })

export function meValue(me: MeDTO | null): MeContextValue {
  return { me, userId: me?.userId }
}

export function useMe(): MeContextValue {
  return useContext(MeContext)
}
