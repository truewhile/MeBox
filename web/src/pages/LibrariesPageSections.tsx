import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ArrowRight, ChevronLeft, ChevronRight, Film, FolderOpen, Library as LibraryIcon, Music, Pin, PlayCircle, RefreshCw, Sparkles, Tv } from 'lucide-react'

import { imageURL } from '../api/client'
import { EpisodeArtworkToggle } from '../components/EpisodeArtworkToggle'
import { MediaCard } from '../components/MediaCard'
import { useInViewOnce } from '../hooks/useInViewOnce'
import { useLazyPreviewBatch } from '../hooks/useLazyPreviewBatch'
import { seriesCardLink } from '../utils/groupSeries'
import { libraryDisplayPath } from './libraryDisplayModel'
import { libraryArtworkItems, type LibraryPreview } from './librariesPageModel'
import { isLibraryPinned } from '../utils/pinnedLibraries'

const TYPE_ICONS: Record<string, ReactNode> = {
  movie: <Film size={18} />,
  tv: <Tv size={18} />,
  anime: <PlayCircle size={18} />,
  variety: <Tv size={18} />,
  music: <Music size={18} />,
  adult: <Film size={18} />,
}
const TYPE_LABELS: Record<string, string> = {
  movie: '电影',
  tv: '剧集',
  anime: '动漫',
  variety: '综艺',
  music: '音乐',
  adult: '成人',
}

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
    <div className="flex flex-col items-center justify-center rounded-3xl border border-dashed border-sand-200 bg-white py-24 text-center">
      <LibraryIcon className="mb-4 h-12 w-12 text-gray-400" />
      <p className="text-sm text-ink-50">暂无媒体库，请到管理后台添加目录。</p>
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
  const pinnedCount = previews.filter((preview) => isLibraryPinned(preview.library.id, pinnedIds)).length
  const queuePreview = useLazyPreviewBatch((ids) => onNeedPreviews?.(ids, 2))

  // 下方媒体库货架支持向下滑动渐进流式加载：默认先展示前 3 个库货架，
  // 随着用户向下滑动接近底部，通过 IntersectionObserver 动态解锁后续媒体库货架。
  const INITIAL_SHELVES = 3
  const STEP_SHELVES = 2
  const [visibleCount, setVisibleCount] = useState(INITIAL_SHELVES)
  const sentinelRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const currentTargets = previews.slice(0, visibleCount).map((p) => p.library.id)
    onNeedPreviews?.(currentTargets, 10)
  }, [previews, visibleCount, onNeedPreviews])

  // 底部哨兵监听与滚动双保险（触底解锁后续媒体库货架）
  useEffect(() => {
    const scrollParent = document.getElementById('app-main-scroll')
    if (!scrollParent) return

    const handleCheckBottom = () => {
      const remaining = scrollParent.scrollHeight - scrollParent.scrollTop - scrollParent.clientHeight
      if (remaining < 600) {
        setVisibleCount((prev) => {
          if (prev >= previews.length) return prev
          return Math.min(prev + STEP_SHELVES, previews.length)
        })
      }
    }

    scrollParent.addEventListener('scroll', handleCheckBottom, { passive: true })

    const sentinel = sentinelRef.current
    let observer: IntersectionObserver | null = null
    if (sentinel) {
      observer = new IntersectionObserver(
        (entries) => {
          const [entry] = entries
          if (entry?.isIntersecting) {
            setVisibleCount((prev) => {
              if (prev >= previews.length) return prev
              return Math.min(prev + STEP_SHELVES, previews.length)
            })
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

    handleCheckBottom()

    return () => {
      scrollParent.removeEventListener('scroll', handleCheckBottom)
      if (observer) {
        observer.disconnect()
      }
    }
  }, [visibleCount, previews.length])

  const visiblePreviews = useMemo(() => {
    return previews.slice(0, visibleCount)
  }, [previews, visibleCount])

  const ENTRY_PAGE_SIZE = 20
  const [entryPage, setEntryPage] = useState(1)
  const totalEntryPages = Math.max(1, Math.ceil(previews.length / ENTRY_PAGE_SIZE))
  const effectiveEntryPage = Math.min(entryPage, totalEntryPages)

  const pagedPreviews = useMemo<LibraryPreview[]>(() => {
    const start = (effectiveEntryPage - 1) * ENTRY_PAGE_SIZE
    return previews.slice(start, start + ENTRY_PAGE_SIZE)
  }, [previews, effectiveEntryPage])

  // 入口网格首屏展示前 20 个库；没有自定义封面的库需要立即拉预览，否则卡片会先渲染占位图标再补图。
  useEffect(() => {
    const ids = pagedPreviews.filter((preview) => !preview.library.cover_url).map((preview) => preview.library.id)
    if (ids.length > 0) {
      onNeedPreviews?.(ids, 2)
    }
  }, [pagedPreviews, onNeedPreviews])


  return (
    <>
      <section className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <h2 className="font-display text-2xl font-bold text-ink-600">媒体库入口</h2>
              {previews.length > ENTRY_PAGE_SIZE && (
                <span className="rounded-md border border-sand-200 bg-sand-50 px-2 py-0.5 text-xs font-semibold text-sand-700">
                  共 {previews.length} 个 · 每页 {ENTRY_PAGE_SIZE} 个
                </span>
              )}
            </div>
            <p className="text-sm text-ink-50">
              按目录进入完整媒体库；下方每个目录也会直接展示最新内容。
              {pinnedCount > 0 ? ` 已置顶 ${pinnedCount} 个媒体库。` : ' 点击卡片右上角图钉可置顶常用媒体库。'}
            </p>
          </div>

          {totalEntryPages > 1 && (
            <div className="flex items-center gap-2 rounded-xl border border-sand-200 bg-white px-2.5 py-1 text-xs shadow-sm">
              <button
                type="button"
                onClick={() => setEntryPage((p) => Math.max(1, p - 1))}
                disabled={effectiveEntryPage <= 1}
                className="rounded-lg p-1 text-ink-50 hover:bg-sand-100 hover:text-ink-600 disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-ink-50 transition-colors"
                title="上一页"
              >
                <ChevronLeft size={14} />
              </button>
              <span className="font-mono text-xs font-semibold text-ink-600">
                {effectiveEntryPage} / {totalEntryPages}
              </span>
              <button
                type="button"
                onClick={() => setEntryPage((p) => Math.min(totalEntryPages, p + 1))}
                disabled={effectiveEntryPage >= totalEntryPages}
                className="rounded-lg p-1 text-ink-50 hover:bg-sand-100 hover:text-ink-600 disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-ink-50 transition-colors"
                title="下一页"
              >
                <ChevronRight size={14} />
              </button>
            </div>
          )}
        </div>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 sm:gap-4 xl:grid-cols-3">
          {pagedPreviews.map((preview) => (
            <div
              key={preview.library.id}
              className="animate-page-in"
            >
              <LibraryEntryCard
                preview={preview}
                pinned={isLibraryPinned(preview.library.id, pinnedIds)}
                onTogglePin={() => onTogglePin(preview.library.id)}
                onVisible={() => {
                  if (!preview.library.cover_url) {
                    queuePreview(preview.library.id)
                  }
                }}
              />
            </div>
          ))}
        </div>
      </section>

      {visiblePreviews.length > 0 && (
        <section className="space-y-6">
          {visiblePreviews.map((preview) => (
            <div
              key={preview.library.id}
              className="animate-page-in"
            >
              <LibraryShelf
                preview={preview}
                pinned={isLibraryPinned(preview.library.id, pinnedIds)}
              />
            </div>
          ))}

          {visibleCount < previews.length && (
            <div ref={sentinelRef} className="flex h-10 w-full items-center justify-center py-2 opacity-60">
              <div className="flex items-center gap-2 text-xs text-[var(--app-muted)]">
                <div className="h-1.5 w-1.5 animate-ping rounded-full bg-brand-500" />
                <span>加载更多媒体库货架…</span>
              </div>
            </div>
          )}
        </section>
      )}
    </>
  )
}

function LibraryEntryCard({
  preview,
  pinned,
  onTogglePin,
  onVisible,
}: {
  preview: LibraryPreview
  pinned: boolean
  onTogglePin: () => void
  onVisible?: () => void
}) {
  const library = preview.library
  const ref = useInViewOnce<HTMLDivElement>(onVisible ?? (() => {}))
  const artwork = library.cover_url
    ? [{ src: library.cover_url, version: library.updated_at }]
    : libraryArtworkItems(preview.cards)
  const displayPath = libraryDisplayPath(library.path)

  return (
    <div
      ref={ref}
      className={
        'group relative flex overflow-hidden rounded-2xl sm:rounded-3xl border bg-white p-2.5 sm:p-3 shadow-card transition-all hover:-translate-y-0.5 hover:shadow-card-hover ' +
        (pinned ? 'border-brand-300 ring-1 ring-brand-100' : 'border-sand-200 hover:border-brand-200')
      }
    >
      <button
        type="button"
        onClick={(event) => {
          event.preventDefault()
          event.stopPropagation()
          onTogglePin()
        }}
        className={
          'absolute right-2 top-2 z-10 rounded-full border p-1.5 transition ' +
          (pinned
            ? 'border-brand-200 bg-brand-50 text-brand-600 hover:bg-brand-100'
            : 'border-sand-200 bg-white/95 text-sand-500 opacity-0 hover:bg-gray-50 hover:text-brand-600 group-hover:opacity-100')
        }
        title={pinned ? '取消置顶' : '置顶媒体库'}
        aria-label={pinned ? '取消置顶' : '置顶媒体库'}
        aria-pressed={pinned}
      >
        <Pin size={14} className={pinned ? 'fill-current' : ''} />
      </button>
      <Link
        to={`/library/${library.id}`}
        className="flex min-w-0 flex-1"
        title={library.name}
      >
      <div className={`grid h-20 w-24 sm:h-24 sm:w-36 shrink-0 gap-1 overflow-hidden rounded-xl sm:rounded-2xl bg-[linear-gradient(135deg,#fff7ed,#f8fafc)] ${artwork.length > 1 ? 'grid-cols-2' : 'grid-cols-1'}`}>
        {artwork.length > 0 ? (
          artwork.map(({ src, version }, index) => (
            <img
              key={`${src}-${index}`}
              src={imageURL(
                src,
                version,
                library.cover_url ? undefined : { maxWidth: 400, maxHeight: 300, quality: 78 },
              )}
              alt=""
              loading="lazy"
              referrerPolicy="no-referrer"
              className="h-full w-full object-cover"
              onError={(event) => { event.currentTarget.style.visibility = 'hidden' }}
            />
          ))
        ) : (
          <div className="col-span-2 flex h-full items-center justify-center text-brand-500">
            {TYPE_ICONS[library.type] ?? <FolderOpen size={28} className="sm:h-8 sm:w-8" />}
          </div>
        )}
      </div>
      <div className="flex min-w-0 flex-1 flex-col justify-between px-3 sm:px-4 py-0.5 sm:py-1">
        <div>
          <div className="mb-1 flex items-center justify-between gap-1.5 pr-8">
            <span className="inline-flex rounded-full bg-sand-100 px-2 py-0.5 text-[10px] font-bold text-sand-600">
              {TYPE_LABELS[library.type] ?? library.type}
            </span>
            <span className="text-[11px] font-semibold text-sand-500 sm:hidden">
              {preview.total.toLocaleString()} 个条目
            </span>
          </div>
          <h2
            className="line-clamp-2 break-words font-display text-sm font-bold leading-snug text-ink-600 group-hover:text-brand-600 sm:text-base lg:text-lg sm:font-black"
            title={library.name}
          >
            {library.name}
            {pinned ? <span className="ml-1.5 align-middle text-[10px] font-bold text-brand-600">置顶</span> : null}
          </h2>
          <p className="mt-0.5 line-clamp-1 break-all text-[11px] text-ink-50 sm:mt-1 sm:text-xs" title={library.path}>
            {displayPath}
          </p>
        </div>
        <div className="mt-1 flex items-center justify-between text-xs font-bold sm:mt-0">
          <span className="hidden text-sand-600 sm:inline">{preview.total.toLocaleString()} 个条目</span>
          <span className="ml-auto inline-flex items-center gap-0.5 text-[11px] text-brand-600 sm:text-xs">
            浏览全部 <ArrowRight size={12} />
          </span>
        </div>
      </div>
      </Link>
    </div>
  )
}

function LibraryShelf({ preview, pinned }: { preview: LibraryPreview; pinned?: boolean }) {
  const library = preview.library
  const cards = preview.cards.slice(0, 10)
  const displayPath = libraryDisplayPath(library.path)

  return (
    <section className="rounded-2xl sm:rounded-[1.7rem] border border-sand-200 bg-white/75 p-3.5 sm:p-4 shadow-card">
      <div className="mb-3 sm:mb-4 flex flex-wrap items-end justify-between gap-2 sm:gap-3">
        <div className="min-w-0">
          <div className="mb-1 inline-flex items-center gap-1.5 sm:gap-2 rounded-full bg-brand-50 px-2 sm:px-2.5 py-0.5 sm:py-1 text-[10px] sm:text-[11px] font-bold text-brand-700">
            {TYPE_ICONS[library.type] ?? <LibraryIcon size={14} />}
            {TYPE_LABELS[library.type] ?? library.type}
          </div>
          <h2
            className="line-clamp-2 break-words font-display text-xl sm:text-2xl font-black text-ink-600"
            title={library.name}
          >
            {library.name}
            {pinned ? <span className="ml-2 align-middle text-xs font-bold text-brand-600">置顶</span> : null}
          </h2>
          <p className="mt-1 line-clamp-1 break-all text-xs text-ink-50">
            <span title={library.path}>{displayPath}</span> · {preview.total.toLocaleString()} 个条目 · 最新 {cards.length} 部
          </p>
        </div>
        <Link to={`/library/${library.id}`} className="btn-outline !px-3 !py-1.5 text-xs sm:!px-4 sm:!py-2.5 sm:text-sm shrink-0">
          浏览全部
          <ArrowRight size={14} />
        </Link>
      </div>

      {cards.length > 0 ? (
        <div className="flex gap-4 overflow-x-auto pb-2 pr-1">
          {cards.map((card) => (
            <div key={card.key} className="w-[9.5rem] shrink-0 lg:w-[10rem] 2xl:w-[10.5rem]">
              <MediaCard
                media={card.rep}
                count={card.count}
                linkTo={seriesCardLink(card)}
              />
            </div>
          ))}
        </div>
      ) : (
        <div className="rounded-2xl border border-dashed border-sand-200 bg-white px-6 py-10 text-center text-sm text-ink-50">
          该目录暂无可展示内容，扫描媒体库后会出现在这里。
        </div>
      )}
    </section>
  )
}
