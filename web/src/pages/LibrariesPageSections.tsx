import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { Library as LibraryIcon, RefreshCw, Sparkles } from 'lucide-react'

import { EpisodeArtworkToggle } from '../components/EpisodeArtworkToggle'
import { HomeLibrariesSection, HomeLibraryRowSection } from './HomePageSections'
import type { LibraryPreview } from './librariesPageModel'

export function LibrariesHeader({
  previewCount,
  total,
  repairMsg,
  repairEpisodeArtwork,
  repairing,
  onRepairEpisodeArtworkChange,
  onRepairRescrape,
  onManageLibraries,
}: {
  previewCount: number
  total: number
  repairMsg: string
  repairEpisodeArtwork: boolean
  repairing: boolean
  onRepairEpisodeArtworkChange: (value: boolean) => void
  onRepairRescrape: () => void
  onManageLibraries: () => void
}) {
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between sm:gap-4">
      <div>
        <h1 className="font-display text-2xl font-bold text-ink-600 sm:text-3xl">媒体库</h1>
        <p className="mt-1 text-xs text-ink-50 sm:text-sm">
          共 {previewCount} 个目录 · {total.toLocaleString()} 个条目。每个目录直接展示最新入库内容。
        </p>
      </div>
      <div className="flex flex-wrap items-center gap-2 sm:gap-3">
        {repairMsg && <span className="w-full text-xs text-ink-50">{repairMsg}</span>}
        <EpisodeArtworkToggle
          checked={repairEpisodeArtwork}
          onChange={onRepairEpisodeArtworkChange}
          title="关闭后仍会获取主海报和每集文字元数据，只跳过每集图片"
          className="h-9 sm:h-10 text-xs sm:text-sm"
        />
        <button
          type="button"
          onClick={onRepairRescrape}
          disabled={repairing}
          className="btn-outline !px-3 !py-1.5 text-xs sm:!px-4 sm:!py-2.5 sm:text-sm disabled:cursor-not-allowed disabled:opacity-60"
          title="从媒体路径回填缺失/错误的外部 ID，再批量重刮整库"
        >
          <RefreshCw size={14} className={repairing ? 'animate-spin' : ''} />
          {repairing ? '正在启动…' : '全库修复+重刮'}
        </button>
        <Link
          to="/scraper/queue"
          className="btn-outline inline-flex items-center gap-1.5 !px-3 !py-1.5 text-xs sm:!px-4 sm:!py-2.5 sm:text-sm"
          title="查看正在进行的刮削任务与进度"
        >
          <Sparkles size={14} className="text-brand-500" />
          <span>刮削队列</span>
        </Link>
        <button
          type="button"
          onClick={onManageLibraries}
          className="btn-outline !px-3 !py-1.5 text-xs sm:!px-4 sm:!py-2.5 sm:text-sm"
        >
          管理媒体库
        </button>
      </div>
    </div>
  )
}

export function LibrariesEmptyState() {
  return (
    <div className="flex flex-col items-center justify-center rounded-3xl border border-dashed border-[var(--app-border)] bg-[var(--app-panel)] py-24 text-center">
      <LibraryIcon className="mb-4 h-12 w-12 text-[var(--app-muted)]" />
      <p className="text-sm text-[var(--app-muted)]">暂无媒体库，请到管理后台添加目录。</p>
    </div>
  )
}

export function LibrariesContent({
  previews,
  pinnedIds,
  onTogglePin,
  onNeedPreviews,
}: {
  previews: LibraryPreview[]
  pinnedIds: string[]
  onTogglePin: (libraryId: string) => void
  onNeedPreviews?: (ids: string[], limit?: number) => void
}) {
  // 下方媒体库货架继续按需解锁：首屏先展示前 3 个，滚动接近底部再加载 2 个。
  const INITIAL_SHELVES = 3
  const STEP_SHELVES = 2
  const [visibleCount, setVisibleCount] = useState(INITIAL_SHELVES)
  const sentinelRef = useRef<HTMLButtonElement | null>(null)
  const userScrolledRef = useRef(false)

  const revealMoreShelves = useCallback(() => {
    setVisibleCount((prev) => {
      if (prev >= previews.length) return prev
      return Math.min(prev + STEP_SHELVES, previews.length)
    })
  }, [previews.length])

  useEffect(() => {
    const currentTargets = previews.slice(0, visibleCount).map((preview) => preview.library.id)
    onNeedPreviews?.(currentTargets, 10)
  }, [previews, visibleCount, onNeedPreviews])

  useEffect(() => {
    const scrollParent = document.getElementById('app-main-scroll')
    if (!scrollParent) return

    const handleCheckBottom = () => {
      if (scrollParent.scrollTop <= 0) return
      userScrolledRef.current = true
      const remaining = scrollParent.scrollHeight - scrollParent.scrollTop - scrollParent.clientHeight
      if (remaining < 600) {
        revealMoreShelves()
      }
    }

    scrollParent.addEventListener('scroll', handleCheckBottom, { passive: true })

    const sentinel = sentinelRef.current
    let observer: IntersectionObserver | null = null
    if (sentinel) {
      observer = new IntersectionObserver(
        (entries) => {
          const [entry] = entries
          if (entry?.isIntersecting && userScrolledRef.current) {
            revealMoreShelves()
          }
        },
        {
          root: scrollParent,
          rootMargin: '600px 0px',
          threshold: 0,
        },
      )
      observer.observe(sentinel)
    }

    return () => {
      scrollParent.removeEventListener('scroll', handleCheckBottom)
      if (observer) observer.disconnect()
    }
  }, [revealMoreShelves, visibleCount, previews.length])

  const visiblePreviews = useMemo(
    () => previews.slice(0, visibleCount),
    [previews, visibleCount],
  )
  const libraries = useMemo(() => previews.map((preview) => preview.library), [previews])
  const libraryData = useMemo(
    () =>
      Object.fromEntries(
        previews.map((preview) => [
          preview.library.id,
          { cards: preview.cards, items: [], total: preview.total },
        ]),
      ),
    [previews],
  )
  const libraryCounts = useMemo(
    () =>
      Object.fromEntries(
        previews.map((preview) => [preview.library.id, preview.total]),
      ),
    [previews],
  )

  return (
    <>
      <HomeLibrariesSection
        libraries={libraries}
        libraryData={libraryData}
        libraryCounts={libraryCounts}
        onNeedPreviews={onNeedPreviews}
        pinnedIds={pinnedIds}
        onTogglePin={onTogglePin}
        showAllLink={false}
        title="媒体库入口"
      />

      {visiblePreviews.length > 0 && (
        <section className="space-y-10">
          {visiblePreviews.map((preview) => (
            <div key={preview.library.id} className="animate-page-in">
              <HomeLibraryRowSection
                library={preview.library}
                cards={preview.cards}
              />
            </div>
          ))}

          {visibleCount < previews.length && (
            <button
              ref={sentinelRef}
              type="button"
              onClick={revealMoreShelves}
              className="flex h-10 w-full items-center justify-center py-2 opacity-60 transition-opacity hover:opacity-100"
            >
              <div className="flex items-center gap-2 text-xs text-[var(--app-muted)]">
                <div className="h-1.5 w-1.5 animate-ping rounded-full bg-brand-500" />
                <span>加载更多媒体库货架…</span>
              </div>
            </button>
          )}
        </section>
      )}
    </>
  )
}
