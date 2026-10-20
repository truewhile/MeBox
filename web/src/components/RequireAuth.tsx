import { Fragment, useEffect, type ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'

import { useAuthStore } from '../stores/auth'
import { usePlayProfileStore } from '../stores/playProfile'
import { libraryCacheScope } from '../utils/libraryCache'
import { hydrateReaderSettings, startReaderSettingsSync } from '../utils/readerSettingsSync'

// Route guard: redirects to /login when no token is present.
export function RequireAuth({ children }: { children: ReactNode }) {
  const token = useAuthStore((s) => s.token)
  const userId = useAuthStore((s) => s.user?.id ?? null)
  // Drop local page state and in-flight callbacks when visibility changes.
  useAuthStore((s) => s.user)
  usePlayProfileStore((s) => s.activeProfileId)
  usePlayProfileStore((s) => s.activeProfilePinToken)
  const visibilityKey = libraryCacheScope()
  const location = useLocation()

  // 阅读器偏好按账号存在服务端：一进入受保护区域就开启回写订阅，并在
  // 登录 / 换账号时重新拉取（这里同时覆盖影视外壳和 /reader/* 独立布局）。
  useEffect(() => {
    startReaderSettingsSync()
  }, [])
  useEffect(() => {
    if (!userId) return
    void hydrateReaderSettings()
  }, [userId])

  if (!token) {
    return <Navigate to="/login" replace state={{ from: location }} />
  }
  return <Fragment key={visibilityKey}>{children}</Fragment>
}

// Route guard: only allows users with role === "admin".
export function RequireAdmin({ children }: { children: ReactNode }) {
  const user = useAuthStore((s) => s.user)
  if (user?.role !== 'admin') {
    return <Navigate to="/" replace />
  }
  return <>{children}</>
}
