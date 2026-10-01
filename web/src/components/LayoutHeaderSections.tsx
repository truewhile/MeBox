import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { ArrowLeft, Film, LoaderCircle, Menu, Search, Star, X } from 'lucide-react'

import { ARTWORK, imageURL } from '../api/client'
import { mediaAPI } from '../api/library'
import { readerAPI, type ReaderBook } from '../api/reader'
import type { Media, PlayProfile, User } from '../types'
import { favouriteMediaLink } from '../utils/mediaNavigation'
import { resolveHeaderBack } from './layoutNavigation'
import { LayoutThemeToggle } from './LayoutThemeToggle'
import { LayoutUserMenu } from './LayoutUserMenu'
import { LayoutReaderModeToggle } from './LayoutReaderModeToggle'
import ReaderBookCover from './ReaderBookCover'
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
  /** 阅读模式：顶部搜索换成书搜索，并保留账号菜单。 */
  readingMode?: boolean
  /** 是否显示顶栏的「影视 / 阅读」图标切换（只在首页）。 */
  showReaderToggle?: boolean
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
  readingMode,
  showReaderToggle,
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
        {readingMode ? <LayoutHeaderBookSearch /> : !hideSearch && <LayoutHeaderSearch />}
      </div>

      {showReaderToggle && <LayoutReaderModeToggle />}

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

// 阅读模式的顶部搜索：先搜书架（本地即时过滤），
// 再给一个「在书源中搜索」的入口跳到多源聚合搜索页。
function LayoutHeaderBookSearch() {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [isOpen, setIsOpen] = useState(false)
  const [books, setBooks] = useState<ReaderBook[] | null>(null)
  const [loading, setLoading] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  const openDropdown = () => {
    setIsOpen(true)
    if (books !== null || loading) return
    setLoading(true)
    readerAPI
      .listBooks()
      .then(setBooks)
      .catch(() => setBooks([]))
      .finally(() => setLoading(false))
  }

  const close = () => setIsOpen(false)

  useEffect(() => {
    if (!isOpen) return
    const onPointerDown = (e: PointerEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) close()
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [isOpen])

  const keyword = query.trim().toLowerCase()
  const matched = (books ?? [])
    .filter(
      (b) =>
        !keyword ||
        b.name.toLowerCase().includes(keyword) ||
        (b.author ?? '').toLowerCase().includes(keyword),
    )
    .slice(0, 8)

  const openBook = (book: ReaderBook) => {
    close()
    setQuery('')
    navigate(`/reader/view/${book.id}`)
  }

  const searchSources = () => {
    const key = query.trim()
    if (!key) return
    close()
    navigate(`/reader/search?key=${encodeURIComponent(key)}`)
  }

  return (
    <div ref={containerRef} className="relative w-full">
      <div className="relative flex items-center">
        <Search
          size={15}
          className="absolute left-3 text-[var(--app-muted)] pointer-events-none transition-colors group-focus-within:text-brand-500 sm:left-3.5 sm:text-[16px]"
        />
        <input
          ref={inputRef}
          type="text"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            setIsOpen(true)
          }}
          onFocus={openDropdown}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              if (matched.length > 0) openBook(matched[0])
              else searchSources()
            } else if (e.key === 'Escape') {
              close()
            }
          }}
          placeholder="搜索书籍…"
          className="w-full h-9 sm:h-10 pl-8 sm:pl-10 pr-8 sm:pr-9 rounded-xl sm:rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] text-xs sm:text-sm text-[var(--app-text)] placeholder:text-[var(--app-muted)] shadow-sm outline-none transition-all duration-200 focus:border-brand-500 focus:ring-2 focus:ring-brand-500/20 focus:bg-[var(--app-panel-elevated)]"
        />
        {loading ? (
          <LoaderCircle size={15} className="absolute right-3.5 text-brand-500 animate-spin" />
        ) : query ? (
          <button
            type="button"
            onClick={() => {
              setQuery('')
              inputRef.current?.focus()
              setIsOpen(true)
            }}
            className="absolute right-3 text-[var(--app-muted)] hover:text-[var(--app-text)] p-0.5 rounded-lg"
            aria-label="清空"
          >
            <X size={15} />
          </button>
        ) : null}
      </div>

      {isOpen && (
        <div className="absolute top-full left-0 right-0 mt-2 max-h-96 overflow-y-auto overscroll-contain rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-2 shadow-2xl z-50 backdrop-blur-xl">
          {books === null || loading ? (
            <div className="flex items-center justify-center gap-2 py-8 text-xs text-[var(--app-muted)]">
              <LoaderCircle size={14} className="text-brand-500 animate-spin" />
              正在加载书架…
            </div>
          ) : matched.length === 0 ? (
            <div className="py-8 text-center text-xs text-[var(--app-muted)]">
              {query.trim() ? `书架里没有与 “${query}” 相关的书` : '书架还是空的，先导入书源或本地书籍'}
            </div>
          ) : (
            <div className="space-y-1">
              <p className="px-2 pb-1 pt-1 text-[10px] font-bold text-[var(--app-muted)]">
                {query.trim() ? '书架匹配' : '书架'}
              </p>
              {matched.map((book) => (
                <button
                  key={book.id}
                  type="button"
                  onPointerDown={(e) => {
                    if (e.button !== 0) return
                    e.preventDefault()
                    openBook(book)
                  }}
                  onClick={() => openBook(book)}
                  className="flex w-full items-center gap-3 rounded-xl p-2 text-left transition-colors hover:bg-[var(--app-hover)] group"
                >
                  <div className="relative h-12 w-9 shrink-0 overflow-hidden rounded-lg bg-[var(--app-panel-soft)]">
                    <ReaderBookCover url={book.cover_url} iconSize={14} />
                  </div>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <p className="truncate text-xs font-bold text-[var(--app-text)] group-hover:text-brand-500">
                        {book.name}
                      </p>
                      {book.is_local && (
                        <span className="shrink-0 rounded border border-[var(--app-border)] bg-[var(--app-panel-elevated)] px-1.5 py-0.5 text-[9px] text-[var(--app-muted)]">
                          本地
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 flex items-center gap-2 text-[10px] text-[var(--app-muted)]">
                      <span className="truncate">{book.author || '佚名'}</span>
                      {book.dur_chapter_title && <span className="truncate">读到 {book.dur_chapter_title}</span>}
                    </div>
                  </div>
                </button>
              ))}
            </div>
          )}

          <button
            type="button"
            onPointerDown={(e) => {
              if (e.button !== 0) return
              e.preventDefault()
              searchSources()
            }}
            onClick={searchSources}
            disabled={!query.trim()}
            className="mt-1 flex w-full items-center gap-2 rounded-xl border border-dashed border-[var(--app-border)] px-3 py-2 text-left text-xs font-bold text-brand-600 transition-colors hover:bg-[var(--app-hover)] disabled:opacity-50"
          >
            <Search size={13} />
            {query.trim() ? `在书源中搜索「${query.trim()}」` : '输入关键词后可在书源中搜索'}
          </button>
        </div>
      )}
    </div>
  )
}

const SEARCH_PAGE_SIZE = 8

function LayoutHeaderSearch() {
  const [query, setQuery] = useState('')
  const [isOpen, setIsOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [results, setResults] = useState<Media[]>([])
  const [hasMore, setHasMore] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const isOpenRef = useRef(false)
  const suppressInputRef = useRef(false)
  // 递增序号守卫：快速连续输入时丢弃过期响应
  const searchSeqRef = useRef(0)
  const pageRef = useRef(0)
  const loadingMoreRef = useRef(false)
  const resultsRef = useRef<Media[]>([])
  const hasMoreRef = useRef(false)
  const navigate = useNavigate()
  const location = useLocation()
  const locationKey = `${location.pathname}${location.search}`
  const prevLocationKeyRef = useRef(locationKey)

  const setSearchOpen = useCallback((open: boolean) => {
    isOpenRef.current = open
    setIsOpen(open)
  }, [])

  const dismissSearch = useCallback((clearQuery: boolean) => {
    suppressInputRef.current = true
    setSearchOpen(false)
    if (clearQuery) {
      searchSeqRef.current += 1
      pageRef.current = 0
      loadingMoreRef.current = false
      resultsRef.current = []
      hasMoreRef.current = false
      setQuery('')
      setResults([])
      setLoading(false)
      setLoadingMore(false)
      setHasMore(false)
    }
    inputRef.current?.blur()
    window.setTimeout(() => {
      suppressInputRef.current = false
    }, 0)
  }, [setSearchOpen])
  const dismissSearchRef = useRef(dismissSearch)
  useEffect(() => {
    dismissSearchRef.current = dismissSearch
  }, [dismissSearch])

  useEffect(() => {
    const trimmed = query.trim()
    if (!trimmed) {
      searchSeqRef.current += 1
      pageRef.current = 0
      loadingMoreRef.current = false
      resultsRef.current = []
      hasMoreRef.current = false
      setResults([])
      setLoading(false)
      setLoadingMore(false)
      setHasMore(false)
      isOpenRef.current = false
      setIsOpen(false)
      return
    }

    const seq = ++searchSeqRef.current
    pageRef.current = 0
    loadingMoreRef.current = false
    resultsRef.current = []
    hasMoreRef.current = false
    setResults([])
    setHasMore(false)
    setLoading(true)
    setLoadingMore(false)
    const timer = setTimeout(async () => {
      try {
        const res = await mediaAPI.searchPage(trimmed, 1, SEARCH_PAGE_SIZE, { groupSeries: true })
        if (seq !== searchSeqRef.current) return
        const items = res.items || []
        const total = res.total ?? items.length
        const more = items.length < total
        pageRef.current = 1
        resultsRef.current = items
        hasMoreRef.current = more
        setResults(items)
        setHasMore(more)
        if (isOpenRef.current && !suppressInputRef.current) setIsOpen(true)
      } catch {
        // 请求失败时保持空结果，用户继续输入或滚动时会重新请求
      } finally {
        if (seq === searchSeqRef.current) setLoading(false)
      }
    }, 250)

    return () => clearTimeout(timer)
  }, [query])

  useLayoutEffect(() => {
    if (prevLocationKeyRef.current === locationKey) return
    prevLocationKeyRef.current = locationKey
    dismissSearchRef.current(true)
  }, [locationKey])

  useEffect(() => {
    function handlePointerDown(e: PointerEvent) {
      if (!isOpenRef.current) return
      const root = containerRef.current
      if (root && e.composedPath().includes(root)) return
      dismissSearchRef.current(false)
    }
    document.addEventListener('pointerdown', handlePointerDown, true)
    return () => document.removeEventListener('pointerdown', handlePointerDown, true)
  }, [])

  const loadMore = async () => {
    const trimmed = query.trim()
    if (!trimmed || loading || loadingMoreRef.current || !hasMoreRef.current) return

    const seq = searchSeqRef.current
    const nextPage = pageRef.current + 1
    loadingMoreRef.current = true
    setLoadingMore(true)

    try {
      const res = await mediaAPI.searchPage(trimmed, nextPage, SEARCH_PAGE_SIZE, { groupSeries: true })
      if (seq !== searchSeqRef.current) return

      const incoming = res.items || []
      const currentResults = resultsRef.current
      const known = new Set(currentResults.map((item) => item.id))
      const nextResults = [...currentResults, ...incoming.filter((item) => !known.has(item.id))]
      const total = res.total ?? nextResults.length
      const more = nextPage * SEARCH_PAGE_SIZE < total
      pageRef.current = nextPage
      resultsRef.current = nextResults
      hasMoreRef.current = more
      setResults(nextResults)
      setHasMore(more)
    } catch {
      // 请求失败时保留当前结果，继续滚动可重试
    } finally {
      if (seq === searchSeqRef.current) {
        loadingMoreRef.current = false
        setLoadingMore(false)
      }
    }
  }

  const handleSelect = (item: Media) => {
    if (suppressInputRef.current) return
    const to = favouriteMediaLink(item)
    dismissSearch(true)
    navigate(to)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      dismissSearch(false)
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
          ref={inputRef}
          type="text"
          value={query}
          onChange={(e) => {
            if (suppressInputRef.current) return
            const nextQuery = e.target.value
            setQuery(nextQuery)
            if (nextQuery.trim() && document.activeElement === inputRef.current) {
              setSearchOpen(true)
            } else if (!nextQuery.trim()) {
              setSearchOpen(false)
            }
          }}
          onFocus={() => {
            if (suppressInputRef.current) return
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
              dismissSearch(true)
            }}
            className="absolute right-3 text-[var(--app-muted)] hover:text-[var(--app-text)] p-0.5 rounded-lg"
          >
            <X size={15} />
          </button>
        ) : null}
      </div>

      {/* Search Dropdown Results */}
      {isOpen && query.trim() && typeof document !== 'undefined'
        ? createPortal(
            <div
              aria-hidden="true"
              className="fixed inset-0 z-[25]"
              onPointerDown={() => dismissSearch(false)}
            />,
            document.body,
          )
        : null}
      {isOpen && query.trim() ? (
          <div
            onScroll={(e) => {
              const target = e.currentTarget
              if (target.scrollHeight - target.scrollTop - target.clientHeight < 80) {
                void loadMore()
              }
            }}
            className="absolute top-full left-0 right-0 mt-2 max-h-96 overflow-y-auto overscroll-contain rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-2 shadow-2xl z-50 backdrop-blur-xl"
          >
            {results.length === 0 && loading ? (
              <div className="flex items-center justify-center gap-2 py-8 text-xs text-[var(--app-muted)]">
                <LoaderCircle size={14} className="text-brand-500 animate-spin" />
                正在搜索…
              </div>
            ) : results.length === 0 ? (
              <div className="py-8 text-center text-xs text-[var(--app-muted)]">
                未搜索到与 “{query}” 相关的媒体内容
              </div>
            ) : (
              <div className="space-y-1">
                {results.map((item) => (
                  <button
                    key={item.id}
                    type="button"
                    onPointerDown={(e) => {
                      if (e.button !== 0) return
                      e.preventDefault()
                      handleSelect(item)
                    }}
                    onClick={() => handleSelect(item)}
                    className="flex w-full items-center gap-3 rounded-xl p-2 text-left transition-colors hover:bg-[var(--app-hover)] group"
                  >
                    <div className="relative h-12 w-9 shrink-0 overflow-hidden rounded-lg bg-[var(--app-panel-soft)]">
                      {item.poster_url ? (
                        <img
                          src={imageURL(item.poster_url, item.updated_at, ARTWORK.posterCard)}
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
                {loadingMore && (
                  <div className="flex items-center justify-center gap-2 py-3 text-[10px] text-[var(--app-muted)]">
                    <LoaderCircle size={12} className="text-brand-500 animate-spin" />
                    正在加载更多…
                  </div>
                )}
                {!loadingMore && !hasMore && results.length > 0 && (
                  <div className="py-2 text-center text-[10px] text-[var(--app-muted)]">
                    已显示全部 {results.length} 条结果
                  </div>
                )}
              </div>
            )}
          </div>
        ) : null}
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
