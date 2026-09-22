import type { LucideIcon } from 'lucide-react'
import {
  Clock,
  FileOutput,
  FolderOpen,
  Heart,
  Home,
  Library,
  ListChecks,
  ListMusic,
  Settings,
  Tv,
  User,
  Users,
} from 'lucide-react'

export type LayoutNavItem = {
  to: string
  label: string
  icon: LucideIcon
  end?: boolean
  permission?: string
  adminOnly?: boolean
}

export type HeaderBackTarget = {
  to: string
  label: string
}

export type MobileBottomNavItem = {
  to: string
  label: string
  icon: LucideIcon
  end?: boolean
  match?: (pathname: string) => boolean
}

/** Desktop admin sidebar and admin mobile drawer. */
export const LAYOUT_NAV_ITEMS: LayoutNavItem[] = [
  { to: '/profile', label: '个人资料', icon: User },
  { to: '/libraries?from=admin', label: '媒体库', icon: Library },
  { to: '/queue', label: '任务队列', icon: ListChecks, adminOnly: true },
  { to: '/admin', label: '用户管理', icon: Users, adminOnly: true },
  { to: '/files', label: '文件管理', icon: FolderOpen, adminOnly: true },
  { to: '/emby-mount', label: 'Emby 挂载', icon: Tv, adminOnly: true },
  { to: '/strm', label: 'STRM 管理', icon: FileOutput, adminOnly: true },
  { to: '/settings', label: '系统设置', icon: Settings, adminOnly: true },
]

/** Media browsing links for the mobile drawer when browsing libraries. */
export const MEDIA_NAV_ITEMS: LayoutNavItem[] = [
  { to: '/', label: '首页', icon: Home, end: true },
  { to: '/libraries', label: '媒体库', icon: Library },
  { to: '/favourites', label: '我的收藏', icon: Heart },
  { to: '/playlists', label: '播放列表', icon: ListMusic },
  { to: '/history', label: '观看历史', icon: Clock },
]

export const MOBILE_BOTTOM_NAV_ITEMS: MobileBottomNavItem[] = [
  { to: '/', label: '首页', icon: Home, end: true },
  {
    to: '/libraries',
    label: '媒体库',
    icon: Library,
    match: (p) => p === '/libraries' || p.startsWith('/library'),
  },
  { to: '/favourites', label: '收藏', icon: Heart },
  {
    to: '/playlists',
    label: '列表',
    icon: ListMusic,
    match: (p) => p === '/playlists' || p.startsWith('/playlist'),
  },
]

const ROOT_MEDIA_PATHS = new Set(['/', '/libraries', '/favourites', '/playlists', '/history'])

/** True only for the fullscreen player route (`/play` or `/play/:id`), not `/playlists`. */
export function isPlayerRoute(pathname: string): boolean {
  return pathname === '/play' || pathname.startsWith('/play/')
}

export function isRootMediaPath(pathname: string): boolean {
  return ROOT_MEDIA_PATHS.has(pathname)
}

/** Admin-entry query used to open the management sidebar on library pages. */
export function isAdminEntrySearch(search: string): boolean {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  return (
    params.get('from') === 'admin' ||
    params.get('from') === 'settings' ||
    params.get('manage') === '1'
  )
}

export function isLibraryPath(pathname: string): boolean {
  return pathname === '/libraries' || pathname.startsWith('/library')
}

/**
 * Sidebar active state must honor query strings. NavLink only matches pathname,
 * so `/libraries` and `/libraries?from=admin` would otherwise both highlight.
 */
export function isSidebarLinkActive(
  to: string,
  pathname: string,
  search: string,
  end?: boolean,
): boolean {
  const [toPath, toQuery = ''] = to.split('?')
  const toIsAdminLibrary = toPath === '/libraries' && isAdminEntrySearch(toQuery)
  const toIsMediaLibrary = toPath === '/libraries' && !toIsAdminLibrary

  if (toIsAdminLibrary) {
    return isLibraryPath(pathname) && isAdminEntrySearch(search)
  }
  if (toIsMediaLibrary) {
    return isLibraryPath(pathname) && !isAdminEntrySearch(search)
  }

  return end ? pathname === toPath : pathname === toPath || pathname.startsWith(`${toPath}/`)
}

export function resolveHeaderBack(pathname: string): HeaderBackTarget | null {
  if (pathname.startsWith('/library/')) {
    return { to: '/libraries', label: '媒体库' }
  }
  // 统计页从观看历史进入，返回链也回到那里，而不是一路跳回首页。
  if (pathname === '/history/stats') {
    return { to: '/history', label: '观看历史' }
  }
  if (pathname === '/playlists' || pathname === '/favourites' || pathname === '/history') {
    return { to: '/', label: '首页' }
  }
  if (pathname.startsWith('/playlist/')) {
    return { to: '/playlists', label: '播放列表' }
  }
  if (pathname.startsWith('/media/')) {
    return null
  }
  if (pathname === '/scraper/queue') {
    return { to: '/queue', label: '任务队列' }
  }
  if (pathname === '/strm/downloads' || pathname === '/strm/uploads') {
    return { to: '/strm', label: 'STRM 管理' }
  }
  if (
    pathname === '/profile' ||
    pathname === '/dlna' ||
    pathname === '/play-profiles' ||
    pathname === '/poster-wall'
  ) {
    return { to: '/', label: '首页' }
  }
  if (
    pathname === '/settings' ||
    pathname === '/admin' ||
    pathname === '/files' ||
    pathname === '/queue' ||
    pathname === '/emby-mount' ||
    pathname === '/strm'
  ) {
    return { to: '/', label: '首页' }
  }
  return null
}

export function shouldShowMobileBottomNav(pathname: string): boolean {
  if (isPlayerRoute(pathname)) return false
  return (
    pathname === '/' ||
    pathname === '/libraries' ||
    pathname.startsWith('/library') ||
    pathname.startsWith('/media') ||
    pathname === '/favourites' ||
    pathname === '/playlists' ||
    pathname.startsWith('/playlist') ||
    pathname === '/history' ||
    pathname === '/poster-wall'
  )
}
