import { useEffect } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'

import { prefetchCommonRouteChunks } from '../appRoutes'
import { useAuthStore } from '../stores/auth'
import { usePlayProfileStore } from '../stores/playProfile'
import {
  LayoutHeader,
  LayoutSidebars,
  LayoutWorkspace,
} from './LayoutSections'
import { MobileBottomNav } from './MobileBottomNav'
import { isAdminEntrySearch, isPlayerRoute, shouldShowMobileBottomNav } from './layoutNavigation'
import { useLayoutPermissions } from './useLayoutPermissions'
import { useLayoutProfiles } from './useLayoutProfiles'
import { useLayoutSidebar } from './useLayoutSidebar'
import { useThemeMode } from './useThemeMode'

function isMediaView(pathname: string, search: string): boolean {
  // 从设置/管理后台菜单进入时展示左侧栏
  if (isAdminEntrySearch(search)) {
    return false
  }

  return (
    pathname === '/' ||
    pathname === '/libraries' ||
    pathname.startsWith('/library') ||
    pathname.startsWith('/media') ||
    isPlayerRoute(pathname) ||
    pathname === '/favourites' ||
    pathname === '/playlists' ||
    pathname.startsWith('/playlist') ||
    pathname === '/history' ||
    pathname === '/poster-wall'
  )
}

export function Layout() {
  const navigate = useNavigate()
  const location = useLocation()

  // 登录后的外壳挂载即开始空闲预取常用页面的路由 chunk，
  // 让首次点击进入各页面时不出现"加载中…"等 chunk 下载。
  useEffect(() => {
    prefetchCommonRouteChunks()
  }, [])

  const user = useAuthStore((s) => s.user)
  const logout = useAuthStore((s) => s.logout)
  const activeProfileId = usePlayProfileStore((s) => s.activeProfileId)
  const setActiveProfile = usePlayProfileStore((s) => s.setActiveProfile)
  const theme = useThemeMode()
  const permissions = useLayoutPermissions(user)
  const sidebar = useLayoutSidebar(location.pathname)
  const profile = useLayoutProfiles({ activeProfileId, setActiveProfile, user })

  const handleLogout = () => { logout(); navigate('/login') }
  const closeProfileAndLogout = () => { profile.setIsProfileOpen(false); handleLogout() }

  const showSidebar = !isMediaView(location.pathname, location.search)
  const hideSearch = location.pathname.startsWith('/settings')
  const isPlayPage = isPlayerRoute(location.pathname)
  const showMobileBottomNav = shouldShowMobileBottomNav(location.pathname)

  return (
    <div className="flex h-[100dvh] min-h-0 w-full overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)] font-body select-none">
      <LayoutSidebars
        sidebar={sidebar}
        isAdmin={permissions.isAdmin}
        can={permissions.can}
        showSidebar={showSidebar}
        sidebarVariant={showSidebar ? 'admin' : 'media'}
      />
      <div className="flex flex-1 flex-col min-w-0 overflow-hidden">
        {!isPlayPage && (
          <LayoutHeader
            permissions={permissions}
            theme={theme}
            onOpenMobileDrawer={() => sidebar.setIsMobileDrawerOpen(true)}
            user={user}
            activeProfileId={activeProfileId}
            profile={profile}
            onLogout={closeProfileAndLogout}
            showSidebar={showSidebar}
            hideSearch={hideSearch}
            pathname={location.pathname}
          />
        )}
        <LayoutWorkspace
          routeKey={location.pathname}
          scrollKey={`${location.pathname}${location.search}`}
          userKey={user?.id}
          showMobileBottomNav={showMobileBottomNav}
        />
        {showMobileBottomNav && (
          <MobileBottomNav onOpenMenu={() => sidebar.setIsMobileDrawerOpen(true)} />
        )}
      </div>
    </div>
  )
}
