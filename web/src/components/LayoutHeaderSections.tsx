import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { AnimatePresence, motion } from 'framer-motion'
import { ArrowLeft, Film, LoaderCircle, Menu, Search, Star, X } from 'lucide-react'

import { imageURL } from '../api/client'
import { mediaAPI } from '../api/library'
import type { Media, PlayProfile, User } from '../types'
import { favouriteMediaLink } from '../utils/mediaNavigation'
import { resolveHeaderBack } from './layoutNavigation'
import { LayoutThemeToggle } from './LayoutThemeToggle'
import { LayoutUserMenu } from './LayoutUserMenu'
import type { useLayoutProfiles } from './useLayoutProfiles'
import type { ThemeMode, useThemeMode } from './useThemeMode'

type LayoutProfileState = ReturnType<typeof useLayoutProfiles>
type LayoutThemeState = ReturnType<typeof useThemeMode>

type LayoutPermissionState = {
  can: (key: string) => boolean
  isAdmin: boolean
}

type LayoutHeaderProps = {
  permissions: LayoutPermissionState
  theme: LayoutThemeState
  onOpenMobileDrawer: () => void
  user: User | null | undefined
  activeProfileId: string | null
  profile: LayoutProfileState
  onLogout: () => void
  showSidebar?: boolean
  hideSearch?: boolean
  pathname?: string
}

export function LayoutHeader({
  permissions,
  theme,
  onOpenMobileDrawer,
  user,
  activeProfileId,
  profile,
  onLogout,
  showSidebar,
  hideSearch,
  pathname = '',
}: LayoutHeaderProps) {
  const navigate = useNavigate()
  const headerBack = resolveHeaderBack(pathname)

  return (
    <header className="relative z-30 flex h-16 shrink-0 items-center justify-between gap-2 border-b border-[var(--app-border)] bg-[var(--app-header-bg)] px-3 backdrop-blur-md sm:h-20 sm:gap-4 sm:px-4 md:px-8">
      {/* Left: back / menu / brand */}
      <div className="flex items-center gap-2 shrink-0 sm:gap-3">
        {headerBack ? (
          <button
            type="button"
            onClick={() => navigate(headerBack.to)}
            className="inline-flex items-center gap-1.5 rounded-xl border border-[var(--app-border)] px-2.5 py-2 text-xs font-semibold text-[var(--app-subtle)] transition-colors hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] lg:hidden"
            title={headerBack.label}
          >
            <ArrowLeft size={16} />
            <span className="max-w-[4.5rem] truncate sm:max-w-none">{headerBack.label}</span>
          </button>
        ) : null}
        <button
          onClick={onOpenMobileDrawer}
          className="rounded-xl border border-[var(--app-border)] p-2.5 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] transition-colors lg:hidden"
          title="打开菜单"
        >
          <Menu size={18} />
        </button>
        <Link to="/" className={`flex items-center gap-2.5 transition-transform hover:scale-105 ${showSidebar ? 'lg:hidden' : 'hidden sm:flex'}`}>
          <img
            src="/brand/logo-192.png"
            alt="MeBox"
            className="h-9 w-9 rounded-xl object-contain shadow-sm"
          />
          <span className="hidden font-display text-lg font-black tracking-tight text-[var(--app-text)] sm:inline-block">
            MeBox
          </span>
        </Link>
      </div>

      {/* Middle: Search Box */}
      <div className="flex min-w-0 flex-1 max-w-xl mx-auto">
        {!hideSearch && <LayoutHeaderSearch />}
      </div>

      {/* Right: Actions (Theme Toggle & User Menu) */}
      <LayoutHeaderActions
        permissions={permissions}
        themeMode={theme.mode}
        onThemeChange={theme.setMode}
        user={user}
        isProfileOpen={profile.isProfileOpen}
        profiles={profile.profiles}
        activeProfileId={activeProfileId}
        activeProfile={profile.activeProfile}
        onToggleProfile={() => profile.setIsProfileOpen((open) => !open)}
        onCloseProfile={() => profile.setIsProfileOpen(false)}
        onUseDefaultProfile={profile.useDefaultProfile}
        onSwitchProfile={profile.switchProfile}
        onLogout={onLogout}
      />
    </header>
  )
}

function LayoutHeaderSearch() {
  const [query, setQuery] = useState('')
  const [isOpen, setIsOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [results, setResults] = useState<Media[]>([])
  const containerRef = useRef<HTMLDivElement>(null)
  const isOpenRef = useRef(false)
  // 递增序号守卫：快速连续输入时丢弃过期响应
  const searchSeqRef = useRef(0)
  const navigate = useNavigate()

  const setSearchOpen = (open: boolean) => {
    isOpenRef.current = open
    setIsOpen(open)
  }

  useEffect(() => {
    const trimmed = query.trim()
    if (!trimmed) {
      searchSeqRef.current += 1
      setResults([])
      setLoading(false)
      isOpenRef.current = false
      setIsOpen(false)
      return
    }

    const seq = ++searchSeqRef.current
    setLoading(true)
    const timer = setTimeout(async () => {
      try {
        const res = await mediaAPI.search(trimmed, 8)
        if (seq !== searchSeqRef.current) return
        setResults(res.items || [])
        if (isOpenRef.current) setIsOpen(true)
      } catch {
        // 请求失败时保留旧结果，避免网络抖动清空下拉
      } finally {
        if (seq === searchSeqRef.current) setLoading(false)
      }
    }, 250)

    return () => clearTimeout(timer)
  }, [query])

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        isOpenRef.current = false
        setIsOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  const handleSelect = (item: Media) => {
    setSearchOpen(false)
    setQuery('')
    navigate(favouriteMediaLink(item))
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      setSearchOpen(false)
    } else if (e.key === 'Enter' && results.length > 0) {
      handleSelect(results[0])
    }
  }

  return (
    <div ref={containerRef} className="relative w-full">
      <div className="relative flex items-center">
        <Search
          size={15}
          className="absolute left-3 text-[var(--app-muted)] pointer-events-none transition-colors group-focus-within:text-brand-500 sm:left-3.5 sm:text-[16px]"
        />
        <input
          type="text"
          value={query}
          onChange={(e) => {
            const nextQuery = e.target.value
            setQuery(nextQuery)
            setSearchOpen(Boolean(nextQuery.trim()))
          }}
          onFocus={() => {
            if (query.trim()) setSearchOpen(true)
          }}
          onKeyDown={handleKeyDown}
          placeholder="搜索媒体…"
          className="w-full h-9 sm:h-10 pl-8 sm:pl-10 pr-8 sm:pr-9 rounded-xl sm:rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] text-xs sm:text-sm text-[var(--app-text)] placeholder:text-[var(--app-muted)] shadow-sm outline-none transition-all duration-200 focus:border-brand-500 focus:ring-2 focus:ring-brand-500/20 focus:bg-[var(--app-panel-elevated)]"
        />
        {loading ? (
          <LoaderCircle size={15} className="absolute right-3.5 text-brand-500 animate-spin" />
        ) : query ? (
          <button
            type="button"
            onClick={() => {
              setQuery('')
              setResults([])
              setSearchOpen(false)
            }}
            className="absolute right-3 text-[var(--app-muted)] hover:text-[var(--app-text)] p-0.5 rounded-lg"
          >
            <X size={15} />
          </button>
        ) : null}
      </div>

      {/* Search Dropdown Results */}
      <AnimatePresence>
        {isOpen && query.trim() && (
          <motion.div
            initial={{ opacity: 0, y: 6 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: 6 }}
            transition={{ duration: 0.15 }}
            className="absolute top-full left-0 right-0 mt-2 max-h-96 overflow-y-auto rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-2 shadow-2xl z-50 backdrop-blur-xl"
          >
            {results.length === 0 && !loading ? (
              <div className="py-8 text-center text-xs text-[var(--app-muted)]">
                未搜索到与 “{query}” 相关的媒体内容
              </div>
            ) : (
              <div className="space-y-1">
                {results.map((item) => (
                  <button
                    key={item.id}
                    onClick={() => handleSelect(item)}
                    className="flex w-full items-center gap-3 rounded-xl p-2 text-left transition-colors hover:bg-[var(--app-hover)] group"
                  >
                    <div className="relative h-12 w-9 shrink-0 overflow-hidden rounded-lg bg-[var(--app-panel-soft)]">
                      {item.poster_url ? (
                        <img
                          src={imageURL(item.poster_url, item.updated_at)}
                          alt=""
                          className="h-full w-full object-cover"
                          loading="lazy"
                        />
                      ) : (
                        <div className="flex h-full w-full items-center justify-center text-[var(--app-muted)]">
                          <Film size={14} />
                        </div>
                      )}
                    </div>

                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <p className="truncate text-xs font-bold text-[var(--app-text)] group-hover:text-brand-500">
                          {item.title}
                        </p>
                        {(item.display_library_name || item.library_name) && (
                          <span className="shrink-0 text-[9px] px-1.5 py-0.5 rounded bg-[var(--app-panel-elevated)] border border-[var(--app-border)] text-[var(--app-muted)] max-w-[130px] truncate">
                            {item.display_library_name || item.library_name}
                          </span>
                        )}
                      </div>
                      <div className="flex items-center gap-2 text-[10px] text-[var(--app-muted)] mt-0.5">
                        {item.year > 0 && <span>{item.year}</span>}
                        {item.rating > 0 && (
                          <span className="flex items-center gap-0.5 text-[#c9954a]">
                            <Star size={9} fill="currentColor" />
                            {item.rating.toFixed(1)}
                          </span>
                        )}
                        {item.video_codec && (
                          <span className="uppercase text-[9px] px-1 py-0.2 rounded border border-[var(--app-border)]">
                            {item.video_codec}
                          </span>
                        )}
                      </div>
                    </div>
                  </button>
                ))}
              </div>
            )}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

type LayoutHeaderActionsProps = {
  permissions: LayoutPermissionState
  themeMode: ThemeMode
  onThemeChange: (mode: ThemeMode) => void
  user: User | null | undefined
  isProfileOpen: boolean
  profiles: PlayProfile[]
  activeProfileId: string | null
  activeProfile: PlayProfile | null
  onToggleProfile: () => void
  onCloseProfile: () => void
  onUseDefaultProfile: () => void
  onSwitchProfile: (profile: PlayProfile) => void
  onLogout: () => void
}

function LayoutHeaderActions({
  themeMode,
  onThemeChange,
  user,
  isProfileOpen,
  profiles,
  activeProfileId,
  activeProfile,
  onToggleProfile,
  onCloseProfile,
  onUseDefaultProfile,
  onSwitchProfile,
  onLogout,
}: LayoutHeaderActionsProps) {
  return (
    <div className="flex shrink-0 items-center gap-2 sm:gap-3 md:gap-4">
      <div className="hidden items-center gap-3 sm:flex md:gap-4">
        <LayoutThemeToggle mode={themeMode} onChange={onThemeChange} />
        <span className="h-6 w-px bg-[var(--app-border)]" />
      </div>
      <LayoutUserMenu
        user={user}
        isOpen={isProfileOpen}
        profiles={profiles}
        activeProfileId={activeProfileId}
        activeProfile={activeProfile}
        onToggle={onToggleProfile}
        onClose={onCloseProfile}
        onUseDefaultProfile={onUseDefaultProfile}
        onSwitchProfile={onSwitchProfile}
        onLogout={onLogout}
        themeMode={themeMode}
        onThemeChange={onThemeChange}
      />
    </div>
  )
}
