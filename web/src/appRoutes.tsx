/* eslint-disable react-refresh/only-export-components */
import { lazy, type ReactElement } from 'react'
import { Navigate } from 'react-router-dom'

const HomePageLoader = () => import('./pages/HomePage').then((m) => ({ default: m.HomePage }))
const LibraryPageLoader = () => import('./pages/LibraryPage').then((m) => ({ default: m.LibraryPage }))
const LibrariesPageLoader = () => import('./pages/LibrariesPage').then((m) => ({ default: m.LibrariesPage }))
const FavouritesPageLoader = () => import('./pages/FavouritesPage').then((m) => ({ default: m.FavouritesPage }))
const PlaylistsPageLoader = () => import('./pages/PlaylistsPage').then((m) => ({ default: m.PlaylistsPage }))
const PlaylistDetailPageLoader = () =>
  import('./pages/PlaylistDetailPage').then((m) => ({ default: m.PlaylistDetailPage }))
const MediaDetailPageLoader = () =>
  import('./pages/MediaDetailPage').then((m) => ({ default: m.MediaDetailPage }))
const PlayerPageLoader = () => import('./pages/PlayerPage').then((m) => ({ default: m.PlayerPage }))
const WatchHistoryPageLoader = () =>
  import('./pages/WatchHistoryPage').then((m) => ({ default: m.WatchHistoryPage }))
const WatchStatsPageLoader = () =>
  import('./pages/WatchStatsPage').then((m) => ({ default: m.WatchStatsPage }))

const HomePage = lazy(HomePageLoader)
const LibraryPage = lazy(LibraryPageLoader)
const LibrariesPage = lazy(LibrariesPageLoader)
const FavouritesPage = lazy(FavouritesPageLoader)
const PlaylistsPage = lazy(PlaylistsPageLoader)
const PlaylistDetailPage = lazy(PlaylistDetailPageLoader)
const MediaDetailPage = lazy(MediaDetailPageLoader)
const PlayerPage = lazy(PlayerPageLoader)
const AdminPage = lazy(() => import('./pages/AdminPage').then((m) => ({ default: m.AdminPage })))
const ProfilePage = lazy(() => import('./pages/ProfilePage').then((m) => ({ default: m.ProfilePage })))
const DlnaPage = lazy(() => import('./pages/DlnaPage').then((m) => ({ default: m.DlnaPage })))
const FileManagerPage = lazy(() =>
  import('./pages/FileManagerPage').then((m) => ({ default: m.FileManagerPage })),
)
const WatchHistoryPage = lazy(WatchHistoryPageLoader)
const WatchStatsPage = lazy(WatchStatsPageLoader)
const PosterWallPage = lazy(() => import('./pages/PosterWallPage').then((m) => ({ default: m.PosterWallPage })))
const ProfileManagementPage = lazy(() =>
  import('./pages/ProfileManagementPage').then((m) => ({ default: m.ProfileManagementPage })),
)
const SettingsPage = lazy(() => import('./pages/SettingsPage').then((m) => ({ default: m.SettingsPage })))
const EmbyMountPage = lazy(() => import('./pages/EmbyMountPage').then((m) => ({ default: m.EmbyMountPage })))
const StrmManagePage = lazy(() => import('./pages/StrmManagePage').then((m) => ({ default: m.StrmManagePage })))
const StrmDownloadQueuePage = lazy(() =>
  import('./pages/StrmQueuePage').then((m) => ({ default: m.StrmDownloadQueuePage })),
)
const StrmUploadQueuePage = lazy(() =>
  import('./pages/StrmQueuePage').then((m) => ({ default: m.StrmUploadQueuePage })),
)
const ScraperQueuePage = lazy(() => import('./pages/ScraperQueuePage').then((m) => ({ default: m.ScraperQueuePage })))
const TaskQueuePage = lazy(() => import('./pages/TaskQueuePage').then((m) => ({ default: m.TaskQueuePage })))

// 常用页面的路由 chunk 空闲预取：应用加载完成后浏览器一空闲就把浏览路径
// （库列表/库详情/媒体详情/收藏/历史等）的 chunk 拉下来，
// 首次点击进入时不再出现"加载中…"等 chunk 下载。失败静默（导航时会重试）。
let prefetchStarted = false
export function prefetchCommonRouteChunks() {
  if (prefetchStarted || typeof window === 'undefined') return

  // 弱网/省流模式不做空闲预取：首屏预览和海报加载优先占用带宽。
  const connection = (navigator as Navigator & {
    connection?: { saveData?: boolean; effectiveType?: string }
  }).connection
  if (
    connection?.saveData ||
    connection?.effectiveType === 'slow-2g' ||
    connection?.effectiveType === '2g'
  ) {
    return
  }

  prefetchStarted = true
  const loaders = [
    LibrariesPageLoader,
    LibraryPageLoader,
    MediaDetailPageLoader,
    FavouritesPageLoader,
    WatchHistoryPageLoader,
    PlaylistsPageLoader,
  ]
  const run = () => {
    for (const load of loaders) {
      load().catch(() => undefined)
    }
  }
  if (typeof requestIdleCallback !== 'undefined') {
    requestIdleCallback(run, { timeout: 8000 })
  } else {
    window.setTimeout(run, 2500)
  }
}

export type AppRoute = {
  path?: string
  index?: boolean
  element: ReactElement
  adminOnly?: boolean
}

export const appRoutes: AppRoute[] = [
  { index: true, element: <HomePage /> },
  { path: 'libraries', element: <LibrariesPage /> },
  { path: 'library/:id', element: <LibraryPage /> },
  { path: 'favourites', element: <FavouritesPage /> },
  { path: 'playlists', element: <PlaylistsPage /> },
  { path: 'playlist/:id', element: <PlaylistDetailPage /> },
  { path: 'media/:id', element: <MediaDetailPage /> },
  { path: 'play/:id', element: <PlayerPage /> },
  { path: 'profile', element: <ProfilePage /> },
  { path: 'dlna', element: <DlnaPage /> },
  { path: 'history', element: <WatchHistoryPage /> },
  { path: 'history/stats', element: <WatchStatsPage /> },
  { path: 'poster-wall', element: <PosterWallPage /> },
  { path: 'play-profiles', element: <ProfileManagementPage /> },
  { path: 'api-configs', element: <Navigate to="/settings?group=api-configs" replace /> },
  { path: 'tools', element: <Navigate to="/files" replace /> },
  { path: 'files', element: <FileManagerPage />, adminOnly: true },
  { path: 'settings', element: <SettingsPage />, adminOnly: true },
  { path: 'emby-mount', element: <EmbyMountPage />, adminOnly: true },
  { path: 'strm', element: <StrmManagePage />, adminOnly: true },
  { path: 'strm/downloads', element: <StrmDownloadQueuePage />, adminOnly: true },
  { path: 'strm/uploads', element: <StrmUploadQueuePage />, adminOnly: true },
  { path: 'scraper/queue', element: <ScraperQueuePage />, adminOnly: true },
  { path: 'queue', element: <TaskQueuePage />, adminOnly: true },
  { path: 'admin', element: <AdminPage />, adminOnly: true },
]
