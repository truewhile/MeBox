import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { libraryAPI } from '../api/library'
import { historyAPI } from '../api/history'
import type { HistoryItem } from '../api/playback'
import type { Library, Media } from '../types'
import type { SeriesCard } from '../utils/groupSeries'
import { fetchLibraries, peekLibraries } from '../utils/libraryCache'
import { usePinnedLibraries } from '../hooks/usePinnedLibraries'
import { sortByPinnedIds } from '../utils/pinnedLibraries'
import {
  ContinueWatchingSection,
  ContinueWatchingSkeleton,
  HomeCarouselSection,
  HomeEmptyState,
  HomeLibrariesSection,
  HomeLibraryRowSection,
  HomeLoadingState,
} from './HomePageSections'

const PREVIEW_BATCH_SIZE = 4
const hasArtwork = (media?: Media | null) => !!(media?.poster_url || media?.backdrop_url)

export function HomePage() {
  const [libraries, setLibraries] = useState<Library[]>([])
  const [libraryData, setLibraryData] = useState<Record<string, { cards: SeriesCard[]; items: Media[]; total: number }>>({})
  const [history, setHistory] = useState<HistoryItem[]>([])
  // 两个请求独立渐进加载：库列表控制整页首屏，播放记录区块自行渲染，
  // 避免整页被 Promise.all 里最慢的那个请求拖住。
  const [librariesLoading, setLibrariesLoading] = useState(true)
  const [historyLoading, setHistoryLoading] = useState(true)
  const { pinnedIds } = usePinnedLibraries()

  // 1. 媒体库元数据极速加载（不带 preview，毫秒级秒开首屏）。
  //    会话内已有缓存则先用缓存立即渲染，后台仍刷新一次兜底。
  useEffect(() => {
    let cancelled = false

    const cached = peekLibraries()
    if (cached && cached.length > 0) {
      setLibraries(cached)
      setLibrariesLoading(false)
    }

    fetchLibraries()
      .then((rows) => (Array.isArray(rows) ? rows : []))
      .catch(() => [] as Library[])
      .then((libs) => {
        if (cancelled) return
        // 失败兜底返回空数组时，不要覆盖已先行渲染的缓存数据
        setLibraries((prev) => (libs.length === 0 && prev.length > 0 ? prev : libs))
        setLibrariesLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [])

  // 2. 继续观看记录独立渐进加载
  useEffect(() => {
    let cancelled = false

    historyAPI
      .continueWatching(12)
      .then((rows) =>
        rows.map(
          (row): HistoryItem => ({
            ...row.history,
            created_at: '',
            updated_at: '',
            media: row.media,
          }),
        ),
      )
      .catch(() => [] as HistoryItem[])
      .then((rows) => {
        if (cancelled) return
        setHistory(rows.filter((h) => h && !h.completed))
        setHistoryLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [])

  const sortedLibraries = useMemo(() => sortByPinnedIds(libraries, pinnedIds), [libraries, pinnedIds])

  // 按需拉取卡片预览管理。同一个库可能先以 4 张封面用于入口网格，
  // 稍后需要 10 张用于内容横排，因此缓存的是已加载数量而不是简单布尔值。
  const fetchedPreviewLimitsRef = useRef<Map<string, number>>(new Map())
  const fetchingPreviewLimitsRef = useRef<Map<string, number>>(new Map())

  const fetchPreviews = useCallback(async (ids: string[], limit = 10) => {
    const uniqueIDs = Array.from(new Set(ids.filter(Boolean)))
    const targets = uniqueIDs.filter(
      (id) =>
        (fetchedPreviewLimitsRef.current.get(id) ?? 0) < limit &&
        (fetchingPreviewLimitsRef.current.get(id) ?? 0) < limit,
    )
    if (targets.length === 0) return
    targets.forEach((id) => fetchingPreviewLimitsRef.current.set(id, limit))

    const batches: string[][] = []
    for (let i = 0; i < targets.length; i += PREVIEW_BATCH_SIZE) {
      batches.push(targets.slice(i, i + PREVIEW_BATCH_SIZE))
    }

    // 分批并发：一个慢库不再阻塞整页预览，先返回的批次立即渲染。
    await Promise.allSettled(
      batches.map(async (batch) => {
        let loaded = false
        try {
          const rows = await libraryAPI.listPreviews(batch, limit)
          loaded = true
          setLibraryData((prev) => {
            const next = { ...prev }
            for (const row of rows) {
              next[row.id] = {
                cards: row.cards ?? [],
                items: [],
                total: row.total ?? 0,
              }
            }
            return next
          })
        } catch {
          // 单个批次失败不影响其他批次；导航回来时会重试。
        } finally {
          batch.forEach((id) => {
            if (loaded) {
              fetchedPreviewLimitsRef.current.set(
                id,
                Math.max(fetchedPreviewLimitsRef.current.get(id) ?? 0, limit),
              )
            }
            if (fetchingPreviewLimitsRef.current.get(id) === limit) {
              fetchingPreviewLimitsRef.current.delete(id)
            }
          })
        }
      }),
    )
  }, [])

  // 3. 首屏只预取轮播和前三行所需的预览；入口卡片进入视口后再按批加载。
  useEffect(() => {
    if (sortedLibraries.length === 0) return
    const carouselLibIds = sortedLibraries
      .filter((l) => l.carousel_enabled === true)
      .map((l) => l.id)
    const topRowLibIds = sortedLibraries.slice(0, 3).map((l) => l.id)
    // 入口网格仍然是一次展示 20 个库；没有自定义封面的库必须在首屏
    // 立即拉预览，否则会先出现占位图标再补图，体感反而更慢。
    const topGridLibIds = sortedLibraries
      .slice(0, 20)
      .filter((l) => !l.cover_url)
      .map((l) => l.id)
    const initialTargets = Array.from(new Set([...carouselLibIds, ...topRowLibIds, ...topGridLibIds]))
    void fetchPreviews(initialTargets, 10)
  }, [sortedLibraries, fetchPreviews])

  // 4. 媒体库展示行渐进流式加载：默认先检视前 3 个库，随向下滚动逐步检视后续库
  const INITIAL_ROWS = 3
  const STEP_ROWS = 2
  const [visibleTargetCount, setVisibleTargetCount] = useState(INITIAL_ROWS)
  const sentinelRef = useRef<HTMLDivElement | null>(null)

  // 随 visibleTargetCount 增加，按需触发后续库的预览加载
  useEffect(() => {
    if (sortedLibraries.length === 0) return
    const currentTargets = sortedLibraries.slice(0, visibleTargetCount).map((l) => l.id)
    void fetchPreviews(currentTargets, 10)
  }, [sortedLibraries, visibleTargetCount, fetchPreviews])

  // 当前已拉取并确认有内容的媒体库行
  const visibleLibraries = useMemo(() => {
    return sortedLibraries
      .slice(0, visibleTargetCount)
      .filter((lib) => (libraryData[lib.id]?.cards?.length ?? 0) > 0)
  }, [sortedLibraries, visibleTargetCount, libraryData])

  const hasMoreLibraries = visibleTargetCount < sortedLibraries.length

  // 底部哨兵监听与滚动双保险（触底解锁后续媒体库行）
  useEffect(() => {
    const scrollParent = document.getElementById('app-main-scroll')
    if (!scrollParent) return

    const handleCheckBottom = () => {
      const remaining = scrollParent.scrollHeight - scrollParent.scrollTop - scrollParent.clientHeight
      if (remaining < 600) {
        setVisibleTargetCount((prev) => {
          if (prev >= sortedLibraries.length) return prev
          return Math.min(prev + STEP_ROWS, sortedLibraries.length)
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
            setVisibleTargetCount((prev) => {
              if (prev >= sortedLibraries.length) return prev
              return Math.min(prev + STEP_ROWS, sortedLibraries.length)
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
  }, [visibleLibraries.length, hasMoreLibraries, sortedLibraries.length])

  // Quick lookup map for libraries
  const libraryMap = useMemo(() => {
    const map = new Map<string, Library>()
    for (const lib of libraries) {
      map.set(lib.id, lib)
    }
    return map
  }, [libraries])

  // Counts lookup
  const libraryCounts = useMemo(() => {
    const counts: Record<string, number> = {}
    for (const lib of libraries) {
      counts[lib.id] = lib.total ?? libraryData[lib.id]?.total ?? 0
    }
    return counts
  }, [libraries, libraryData])

  // Compute items to show in the Hero Carousel. Only libraries flagged
  // carousel_enabled contribute; a library's items array is empty for
  // series-type libs (loaded via /series), so gate on cards instead.
  const carouselItems = useMemo(() => {
    const candidateMedia: Media[] = []
    const effectiveSelectedIds = libraries
      .filter((l) => l.carousel_enabled === true)
      .map((l) => l.id)

    for (const libId of effectiveSelectedIds) {
      const data = libraryData[libId]
      if (data && data.cards.length > 0) {
        for (const card of data.cards) {
          if (hasArtwork(card.rep)) {
            candidateMedia.push(card.rep)
          }
        }
      }
    }

    // Fallback to all loaded items with artwork
    if (candidateMedia.length === 0) {
      for (const lib of libraries) {
        const data = libraryData[lib.id]
        if (data) {
          for (const card of data.cards) {
            candidateMedia.push(card.rep)
          }
        }
      }
    }

    return candidateMedia.slice(0, 10)
  }, [libraries, libraryData])

  // 库列表还没回来先展示整页 loading；库为空时再等一下播放记录，
  // 以免在"空站点"和"有观看记录"两个终态之间闪空白。
  if (librariesLoading || (libraries.length === 0 && historyLoading)) {
    return <HomeLoadingState />
  }

  const empty =
    libraries.length === 0 &&
    history.length === 0

  if (empty) {
    return <HomeEmptyState />
  }

  return (
    <div className="space-y-12 pb-16">
      {/* 1. 顶部海报轮播区 */}
      {carouselItems.length > 0 && (
        <HomeCarouselSection items={carouselItems} libraryMap={libraryMap} />
      )}

      {/* 2. 继续观看（独立加载，未回来前展示同构骨架） */}
      {historyLoading && <ContinueWatchingSkeleton />}
      {!historyLoading && history.length > 0 && <ContinueWatchingSection history={history} />}

      {/* 3. 媒体库卡片区（每页展示 20 个媒体库） */}
      {sortedLibraries.length > 0 && (
        <HomeLibrariesSection
          libraries={sortedLibraries}
          libraryData={libraryData}
          libraryCounts={libraryCounts}
          onNeedPreviews={fetchPreviews}
        />
      )}

      {/* 4. 各媒体库内容展示行（向下滑动渐进流式加载，不受上方20个分页限制） */}
      {visibleLibraries.length > 0 && (
        <div className="space-y-10">
          {visibleLibraries.map((lib) => {
            const cards = libraryData[lib.id]?.cards || []
            return (
              <HomeLibraryRowSection
                key={lib.id}
                library={lib}
                cards={cards}
              />
            )
          })}
          {hasMoreLibraries && (
            <div ref={sentinelRef} className="flex h-10 w-full items-center justify-center py-2 opacity-60">
              <div className="flex items-center gap-2 text-xs text-[var(--app-muted)]">
                <div className="h-1.5 w-1.5 animate-ping rounded-full bg-brand-500" />
                <span>加载更多媒体库…</span>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
