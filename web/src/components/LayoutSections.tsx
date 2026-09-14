import { Outlet } from 'react-router-dom'
import clsx from 'clsx'

import { LayoutSidebarContent, type LayoutSidebarContentProps } from './LayoutSidebarContent'
import { isPlayerRoute } from './layoutNavigation'
import { RouteErrorBoundary } from './RouteErrorBoundary'
import { useScrollMemory } from '../hooks/useScrollMemory'
import type { useLayoutSidebar } from './useLayoutSidebar'

type LayoutSidebarState = ReturnType<typeof useLayoutSidebar>

type LayoutSidebarProps = {
  children: React.ReactNode
  isSidebarOpen: boolean
}

type LayoutMobileSidebarProps = {
  children: React.ReactNode
  isOpen: boolean
  onClose: () => void
}

type LayoutSidebarsProps = Omit<
  LayoutSidebarContentProps,
  'isSidebarOpen' | 'isMobileDrawerOpen' | 'onToggleSidebar' | 'onCloseMobileDrawer'
> & {
  sidebar: LayoutSidebarState
  showSidebar: boolean
  sidebarVariant?: 'media' | 'admin'
}

type LayoutWorkspaceProps = {
  routeKey: string
  userKey?: string
  showMobileBottomNav?: boolean
}

export { LayoutHeader } from './LayoutHeaderSections'

export function LayoutDesktopSidebar({ children, isSidebarOpen }: LayoutSidebarProps) {
  return (
    <aside
      className={clsx(
        'hidden lg:flex min-h-0 h-full shrink-0 flex-col transition-all duration-300 ease-out',
        isSidebarOpen ? 'w-64' : 'w-20',
      )}
    >
      {children}
    </aside>
  )
}

export function LayoutMobileSidebar({ children, isOpen, onClose }: LayoutMobileSidebarProps) {
  if (!isOpen) return null

  return (
    <div className="fixed inset-0 z-50 flex lg:hidden">
      <div
        onClick={onClose}
        className="fixed inset-0 bg-black/15 backdrop-blur-sm animate-overlay-in"
      />
      <div className="relative z-10 flex h-full min-h-0 w-64 max-w-xs flex-col overflow-hidden shadow-xl animate-drawer-in">
        {children}
      </div>
    </div>
  )
}

export function LayoutSidebars({
  sidebar,
  isAdmin,
  can,
  showSidebar,
  sidebarVariant = 'admin',
}: LayoutSidebarsProps) {
  const content = (
    <LayoutSidebarContent
      isSidebarOpen={sidebar.isSidebarOpen}
      isMobileDrawerOpen={sidebar.isMobileDrawerOpen}
      isAdmin={isAdmin}
      can={can}
      onToggleSidebar={sidebar.toggleSidebar}
      onCloseMobileDrawer={() => sidebar.setIsMobileDrawerOpen(false)}
      variant={sidebarVariant}
    />
  )

  if (!showSidebar) {
    return (
      <>
        <LayoutMobileSidebar
          isOpen={sidebar.isMobileDrawerOpen}
          onClose={() => sidebar.setIsMobileDrawerOpen(false)}
        >
          {content}
        </LayoutMobileSidebar>
      </>
    )
  }

  return (
    <>
      <LayoutDesktopSidebar isSidebarOpen={sidebar.isSidebarOpen}>{content}</LayoutDesktopSidebar>
      <LayoutMobileSidebar
        isOpen={sidebar.isMobileDrawerOpen}
        onClose={() => sidebar.setIsMobileDrawerOpen(false)}
      >
        {content}
      </LayoutMobileSidebar>
    </>
  )
}

export function LayoutWorkspace({
  routeKey,
  userKey = 'anonymous',
  showMobileBottomNav = false,
}: LayoutWorkspaceProps) {
  useScrollMemory(routeKey, userKey)

  const bottomPad = showMobileBottomNav
    ? 'pb-[calc(3.75rem+env(safe-area-inset-bottom,0px))] lg:pb-10'
    : ''

  if (isPlayerRoute(routeKey)) {
    return (
      <main className="flex flex-1 h-full w-full overflow-hidden">
        <RouteErrorBoundary>
          <Outlet />
        </RouteErrorBoundary>
      </main>
    )
  }

  return (
    <main id="app-main-scroll" className={clsx('flex-1 overflow-y-auto [overflow-anchor:none] px-4 py-6 md:px-8 md:py-10', bottomPad)}>
      <div className="max-w-7xl mx-auto">
        <div key={routeKey} className="animate-page-in">
          <RouteErrorBoundary>
            <Outlet />
          </RouteErrorBoundary>
        </div>
      </div>
    </main>
  )
}

export { LayoutSidebarContent }
