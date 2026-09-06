import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link, useLocation } from 'react-router-dom'
import { AnimatePresence, motion } from 'framer-motion'
import { Cast, ChevronDown, Clock, Heart, ListMusic, LogOut, Settings, Tv, UserCog } from 'lucide-react'
import clsx from 'clsx'

import type { PlayProfile } from '../types'
import { LayoutThemeToggle } from './LayoutThemeToggle'
import { TemporaryPasswordDialog } from './TemporaryPasswordDialog'
import type { ThemeMode } from './useThemeMode'

type MenuPosition = {
  top: number
  right: number
}

type LayoutUser = {
  username?: string
  role?: string
}

type LayoutUserMenuProps = {
  user: LayoutUser | null | undefined
  isOpen: boolean
  profiles: PlayProfile[]
  activeProfileId: string | null
  activeProfile: PlayProfile | null
  onToggle: () => void
  onClose: () => void
  onUseDefaultProfile: () => void
  onSwitchProfile: (profile: PlayProfile) => void
  onLogout: () => void
  themeMode?: ThemeMode
  onThemeChange?: (mode: ThemeMode) => void
}

export function LayoutUserMenu({
  user,
  isOpen,
  profiles,
  activeProfileId,
  activeProfile,
  onToggle,
  onClose,
  onUseDefaultProfile,
  onSwitchProfile,
  onLogout,
  themeMode,
  onThemeChange,
}: LayoutUserMenuProps) {
  const location = useLocation()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const onCloseRef = useRef(onClose)
  const [menuPosition, setMenuPosition] = useState<MenuPosition | null>(null)
  const [isOtpOpen, setIsOtpOpen] = useState(false)

  onCloseRef.current = onClose

  const updateMenuPosition = useCallback(() => {
    const trigger = triggerRef.current
    if (!trigger) return
    const rect = trigger.getBoundingClientRect()
    setMenuPosition({
      top: rect.bottom + 12,
      right: Math.max(8, window.innerWidth - rect.right),
    })
  }, [])

  useLayoutEffect(() => {
    if (!isOpen) {
      setMenuPosition(null)
      return undefined
    }

    updateMenuPosition()
    window.addEventListener('resize', updateMenuPosition)
    window.addEventListener('scroll', updateMenuPosition, true)
    return () => {
      window.removeEventListener('resize', updateMenuPosition)
      window.removeEventListener('scroll', updateMenuPosition, true)
    }
  }, [isOpen, updateMenuPosition])

  useEffect(() => {
    if (!isOpen) return undefined

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCloseRef.current()
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [isOpen])

  useEffect(() => {
    onCloseRef.current()
  }, [location.pathname, location.search])

  const menuPortal = isOpen && menuPosition && typeof document !== 'undefined'
    ? createPortal(
        <AnimatePresence>
          <motion.div
            key="layout-user-menu-backdrop"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.12 }}
            className="fixed inset-0 z-[120]"
            aria-hidden="true"
            onPointerDown={onClose}
          />
          <motion.div
            key="layout-user-menu-panel"
            initial={{ opacity: 0, y: 10, scale: 0.95 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 10, scale: 0.95 }}
            transition={{ duration: 0.15 }}
            role="menu"
            style={{ top: menuPosition.top, right: menuPosition.right }}
            className="fixed z-[121] w-56 origin-top-right rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-2 shadow-xl"
            onPointerDown={(event) => event.stopPropagation()}
          >
	            <UserMenuLink to="/profile" icon={<Settings size={16} />} label="设置" onNavigate={onClose} />
	            <UserMenuLink to="/favourites" icon={<Heart size={16} />} label="我的收藏" onNavigate={onClose} />
	            <UserMenuLink to="/playlists" icon={<ListMusic size={16} />} label="播放列表" onNavigate={onClose} />
	            <UserMenuLink to="/history" icon={<Clock size={16} />} label="观看历史" onNavigate={onClose} />
	            <UserMenuLink to="/dlna" icon={<Cast size={16} />} label="DLNA投屏" onNavigate={onClose} />
	            <button
	              type="button"
	              onClick={() => {
	                onClose()
	                setIsOtpOpen(true)
	              }}
	              className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-sm text-[var(--app-subtle)] transition-colors hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
	            >
	              <Tv size={16} />
	              <span>电视端临时登录码</span>
	            </button>
            {themeMode && onThemeChange ? (
              <div className="px-3 py-2 sm:hidden">
                <p className="mb-2 text-[10px] font-bold uppercase tracking-wider text-[var(--app-muted)]">
                  主题
                </p>
                <LayoutThemeToggle mode={themeMode} onChange={onThemeChange} />
              </div>
            ) : null}
            <div className="my-1.5 border-t border-[var(--app-border)]" />
            <div className="px-3 py-2">
              <p className="mb-2 text-[10px] font-bold uppercase tracking-wider text-[var(--app-muted)]">
                当前观影 Profile
              </p>
              <div className="space-y-1">
                <button
                  onClick={onUseDefaultProfile}
                  className={profileButtonClass(!activeProfileId)}
                >
                  <span>账号默认</span>
                  <span>{!activeProfileId ? '使用中' : ''}</span>
                </button>
                {profiles.map((profile) => (
                  <button
                    key={profile.id}
                    onClick={() => onSwitchProfile(profile)}
                    className={profileButtonClass(activeProfileId === profile.id)}
                  >
                    <span className="truncate">{profile.name}</span>
                    <span className="ml-2 shrink-0">{profile.allow_adult ? '成人' : '安全'}</span>
                  </button>
                ))}
              </div>
            </div>
            <UserMenuLink
              to="/play-profiles"
              icon={<UserCog size={16} />}
              label="管理观影 Profile"
              onNavigate={onClose}
            />
            <div className="my-1.5 border-t border-[var(--app-border)]" />
            <button
              onClick={() => {
                onClose()
                onLogout()
              }}
              className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-sm text-red-500 transition-colors hover:bg-[var(--app-danger-soft)]"
            >
              <LogOut size={16} />
              <span>安全登出系统</span>
            </button>
          </motion.div>
        </AnimatePresence>,
        document.body,
      )
    : null

  return (
    <div className="relative z-[122]" data-testid="layout-user-menu">
      <button
        ref={triggerRef}
        onClick={onToggle}
        aria-expanded={isOpen}
        aria-haspopup="menu"
        className="relative z-[122] flex items-center gap-2.5 rounded-full border border-[var(--app-border)] p-1 pr-3 transition-all hover:bg-[var(--app-hover)]"
      >
        <div className="flex h-8 w-8 items-center justify-center rounded-full bg-gradient-to-br from-[#111827] to-[#1f2937] font-display text-xs font-bold text-white shadow-sm">
          {user?.username?.slice(0, 2).toUpperCase() || 'US'}
        </div>
        <div className="hidden text-left md:block">
          <p className="text-xs font-bold leading-none text-[var(--app-text)]">{user?.username}</p>
          <p className="mt-0.5 text-[9px] font-bold uppercase leading-none tracking-wider text-[var(--app-muted)]">
            {activeProfile ? `Profile: ${activeProfile.name}` : user?.role}
          </p>
        </div>
        <ChevronDown size={14} className="text-[var(--app-muted)]" />
      </button>
      {menuPortal}
      <TemporaryPasswordDialog isOpen={isOtpOpen} onClose={() => setIsOtpOpen(false)} />
    </div>
  )
}

function UserMenuLink({
  to,
  icon,
  label,
  onNavigate,
}: {
  to: string
  icon: ReactNode
  label: string
  onNavigate: () => void
}) {
  return (
    <Link
      to={to}
      onClick={onNavigate}
      className="flex items-center gap-3 rounded-xl px-3 py-2 text-sm text-[var(--app-subtle)] transition-colors hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
    >
      {icon}
      <span>{label}</span>
    </Link>
  )
}

function profileButtonClass(active: boolean): string {
  return clsx(
    'flex w-full items-center justify-between rounded-xl px-2.5 py-2 text-left text-xs transition-colors',
    active
      ? 'bg-[var(--app-active-bg)] text-[var(--app-active-text)]'
      : 'text-[var(--app-subtle)] hover:bg-[var(--app-hover)]',
  )
}
