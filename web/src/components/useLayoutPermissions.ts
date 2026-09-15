import { useCallback, useEffect } from 'react'

import { usePermissionStore } from '../stores/permissions'
import type { User } from '../types'

export function useLayoutPermissions(user: User | null | undefined) {
  const permissions = usePermissionStore((state) => state.permissions)
  const isPermissionLoading = usePermissionStore((state) => state.isLoading)
  const fetchPermissions = usePermissionStore((state) => state.fetchPermissions)

  useEffect(() => {
    if (user && !isPermissionLoading && Object.keys(permissions ?? {}).length === 0) {
      fetchPermissions().catch(() => undefined)
    }
  }, [fetchPermissions, isPermissionLoading, permissions, user])

  const isAdmin = user?.role === 'admin'
  const can = useCallback(
    (key: string) => isAdmin || (permissions ?? {})[key] === true,
    [isAdmin, permissions],
  )

  return { can, isAdmin }
}
