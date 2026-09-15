import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import toast from 'react-hot-toast'

import { libraryAPI } from '../api/library'
import type { Library, Media } from '../types'
import { peekLibrary, resolveLibrary } from '../utils/libraryCache'
import { groupSeries, isEpisodeLike, type SeriesCard } from '../utils/groupSeries'
import type { SortField, SortOrder } from '../utils/mediaSort'
import { MAX_RESTORE_PAGES, readListPosition, writeListPosition } from '../hooks/useListPositionMemory'

export function useLibraryData(
  libraryID: string,
  selectedSeries: SeriesCard | null,
  sortField: SortField,
  sortOrder: SortOrder,
) {
  const [library, setLibrary] = useState<Library | null>(null)
  const [items, setItems] = useState<Media[]>([])
  const [serverSeriesCards, setServerSeriesCards] = useState<SeriesCard[]>([])
  const [seriesEpisodeItems, setSeriesEpisodeItems] = useState<Media[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [hasMore, setHasMore] = useState(false)
  const [loadingSeriesEpisodes, setLoadingSeriesEpisodes] = useState(false)

  const isSeriesLibrary = isSeriesLibraryType(library?.type)
  const hasEpisodicItems = useMemo(() => items.some(isEpisodeLike), [items])
  const isSeries = isSeriesLibrary || serverSeriesCards.length > 0 || hasEpisodicItems

  const seriesCards = useMemo(() => {
    if (isSeriesLibrary) return serverSeriesCards
    if (!isSeries || items.length === 0) return []
    return groupSeries(items)
  }, [isSeries, isSeriesLibrary, items, serverSeriesCards])

  const [reloadTick, setReloadTick] = useState(0)
  const requestSeqRef = useRef(0)
  const nextPageRef = useRef(2)
  const loadedCountRef = useRef(0)
  const totalRef = useRef(0)
  const hasMoreRef = useRef(false)
  const libraryRef = useRef<Library | null>(null)
  const modeRef = useRef<'media' | 'series'>('media')
  const moreInFlightRef = useRef(false)

  // 分页位置按「媒体库 + 排序」记忆：详情页返回、甚至换完排序再切回来，
  // 都能回到上次加载到的页数。
  const positionKey = `library:${libraryID}:${sortField}:${sortOrder}`

  // 拉取并追加下一页。滚动哨兵、按钮和首屏分页恢复共用这一条路径，
  // 避免两套分页逻辑各自算页码。
  const appendNextPage = useCallback(async (options?: { remember?: boolean }) => {
    const lib = libraryRef.current
    if (!lib || !hasMoreRef.current) return false
    const seq = requestSeqRef.current
    const page = nextPageRef.current
    try {
      if (modeRef.current === 'series') {
        const data = await libraryAPI.listSeries(libraryID, page, pageSizeFor(lib), {
          sort: sortField,
          order: sortOrder,
        })
        if (seq !== requestSeqRef.current) return false
        const pageItems = data.items ?? []
        setServerSeriesCards((prev) => [...prev, ...pageItems])
        loadedCountRef.current += pageItems.length
        totalRef.current = data.total ?? totalRef.current
        nextPageRef.current = page + 1
        hasMoreRef.current = pageItems.length > 0 && loadedCountRef.current < totalRef.current
      } else {
        const data = await libraryAPI.listMedia(libraryID, page, pageSizeFor(lib), {
          sort: sortField,
          order: sortOrder,
        })
        if (seq !== requestSeqRef.current) return false
        const pageItems = data.items ?? []
        setItems((prev) => [...prev, ...pageItems])
        loadedCountRef.current += pageItems.length
        totalRef.current = data.total ?? totalRef.current
        nextPageRef.current = page + 1
        hasMoreRef.current = pageItems.length > 0 && loadedCountRef.current < totalRef.current
      }
      setTotal(totalRef.current)
      setHasMore(hasMoreRef.current)
      if (options?.remember !== false) writeListPosition(positionKey, page)
      return true
    } catch {
      if (seq === requestSeqRef.current) {
        toast.error('媒体库加载失败')
        hasMoreRef.current = false
        setHasMore(false)
      }
      return false
    }
  }, [libraryID, positionKey, sortField, sortOrder])

  const loadMore = useCallback(async (options?: { remember?: boolean }) => {
    if (moreInFlightRef.current || !hasMoreRef.current) return
    moreInFlightRef.current = true
    setLoadingMore(true)
    try {
      await appendNextPage(options)
    } finally {
      moreInFlightRef.current = false
      setLoadingMore(false)
    }
  }, [appendNextPage])

  const loadAll = useCallback(async () => {
    const seq = requestSeqRef.current
    setLoadingMore(true)
    try {
      while (seq === requestSeqRef.current && hasMoreRef.current) {
        // 一次拉全量（random 排序前的准备）不代表用户的分页位置，不写入记忆。
        await loadMore({ remember: false })
        if (seq !== requestSeqRef.current || !hasMoreRef.current) break
        await yieldToBrowser()
      }
    } finally {
      if (seq === requestSeqRef.current) setLoadingMore(false)
    }
  }, [loadMore])

  useEffect(() => {
    if (!libraryID) return
    const seq = ++requestSeqRef.current
    let cancelled = false
    nextPageRef.current = 2
    loadedCountRef.current = 0
    totalRef.current = 0
    hasMoreRef.current = false
    moreInFlightRef.current = false
    setLoading(true)
    setLoadingMore(false)
    setHasMore(false)
    setLibrary(null)
    setItems([])
    setServerSeriesCards([])
    setSeriesEpisodeItems([])
    setTotal(0)

    const bootstrap = async () => {
      let lib = peekLibrary(libraryID) ?? null
      if (lib) {
        libraryRef.current = lib
        setLibrary(lib)
      }
      try {
        const resolved = await resolveLibrary(libraryID)
        if (cancelled || seq !== requestSeqRef.current) return
        if (!lib) {
          lib = resolved
          libraryRef.current = lib
          setLibrary(lib)
        }
      } catch {
        if (!cancelled && seq === requestSeqRef.current) {
          setLoading(false)
          toast.error('媒体库不存在或无权限')
        }
        return
      }
      if (!lib || cancelled || seq !== requestSeqRef.current) return

      const seriesMode = isSeriesLibraryType(lib.type)
      modeRef.current = seriesMode ? 'series' : 'media'
      const pageSize = pageSizeFor(lib)
      try {
        if (seriesMode) {
          const data = await libraryAPI.listSeries(libraryID, 1, pageSize, { sort: sortField, order: sortOrder })
          if (cancelled || seq !== requestSeqRef.current) return
          const pageItems = data.items ?? []
          setServerSeriesCards(pageItems)
          loadedCountRef.current = pageItems.length
          totalRef.current = data.total ?? pageItems.length
        } else {
          const data = await libraryAPI.listMedia(libraryID, 1, pageSize, { sort: sortField, order: sortOrder })
          if (cancelled || seq !== requestSeqRef.current) return
          const pageItems = data.items ?? []
          setItems(pageItems)
          loadedCountRef.current = pageItems.length
          totalRef.current = data.total ?? pageItems.length
        }
        setTotal(totalRef.current)
        hasMoreRef.current = loadedCountRef.current < totalRef.current
        setHasMore(hasMoreRef.current)

        // 按记忆的分页位置把后续页补回来：从详情页返回时直接回到上次翻到的
        // 位置，而不是只剩第一页。超出上限的部分继续交给底部哨兵按需加载。
        const restoreTarget = Math.min(readListPosition(positionKey, 1), MAX_RESTORE_PAGES)
        while (
          !cancelled &&
          seq === requestSeqRef.current &&
          hasMoreRef.current &&
          nextPageRef.current <= restoreTarget
        ) {
          const advanced = await appendNextPage()
          if (!advanced) break
        }
      } catch {
        if (!cancelled && seq === requestSeqRef.current) {
          toast.error('媒体库加载失败')
        }
      } finally {
        if (!cancelled && seq === requestSeqRef.current) {
          setLoading(false)
        }
      }
    }

    void bootstrap()
    return () => {
      cancelled = true
      requestSeqRef.current += 1
    }
  }, [appendNextPage, libraryID, positionKey, reloadTick, sortField, sortOrder])

  useEffect(() => {
    if (!libraryID || !isSeriesLibrary || !selectedSeries) {
      setSeriesEpisodeItems([])
      setLoadingSeriesEpisodes(false)
      return
    }
    let cancelled = false
    setLoadingSeriesEpisodes(true)
    setSeriesEpisodeItems([])
    libraryAPI.listSeriesEpisodes(libraryID, selectedSeries.key)
      .then((r) => {
        if (!cancelled) setSeriesEpisodeItems(r.items ?? [])
      })
      .catch(() => {
        if (!cancelled) toast.error('剧集列表加载失败')
      })
      .finally(() => {
        if (!cancelled) setLoadingSeriesEpisodes(false)
      })
    return () => { cancelled = true }
  }, [libraryID, isSeriesLibrary, selectedSeries])

  const reloadCurrentLibrary = useCallback(() => {
    setReloadTick((tick) => tick + 1)
  }, [])

  const loadedCount = isSeriesLibrary ? serverSeriesCards.length : items.length
  const loadingAllText =
    !loading && hasMore
      ? `已加载 ${loadedCount} / ${total}，向下滚动继续加载`
      : ''

  return {
    library,
    items,
    seriesEpisodeItems,
    total,
    loading,
    loadingMore,
    hasMore,
    loadMore,
    loadAll,
    loadingSeriesEpisodes,
    isSeriesLibrary,
    isSeries,
    seriesCards,
    loadingAllText,
    reloadCurrentLibrary,
  }
}

function isSeriesLibraryType(type?: string) {
  return type === 'tv' || type === 'anime' || type === 'variety'
}

function pageSizeFor(lib: Library): number {
  // 首屏保持一屏多一点，后续统一由底部哨兵按页追加。此前沿用旧的
  // 500/2000 批量值，会让中小型媒体库在第一次请求时就等于全量加载。
  if (lib.is_remote_emby) return 50
  return 60
}

function yieldToBrowser(): Promise<void> {
  return new Promise((resolve) => {
    if (typeof requestIdleCallback !== 'undefined') {
      requestIdleCallback(() => resolve(), { timeout: 48 })
    } else {
      setTimeout(resolve, 0)
    }
  })
}
