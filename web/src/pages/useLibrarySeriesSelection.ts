import { useEffect, useMemo, useState } from 'react'

import type { Media } from '../types'
import {
  expandSeriesMediaVersions,
  getSeriesKey,
  isTheatricalFeature,
  seasonSortOrder,
  specialSectionForMedia,
  type SeriesCard,
} from '../utils/groupSeries'
import { resolveSeriesCardByKey } from '../utils/seriesCardResolve'

type SeasonEpisodes = {
  season: number
  episodes: Media[]
}

type UseLibrarySeriesSelectionOptions = {
  libraryID: string
  items: Media[]
  seriesEpisodeItems: Media[]
  isSeriesLibrary: boolean
  isSeries: boolean
  loading: boolean
  seriesCards: SeriesCard[]
  searchParams: URLSearchParams
  setSearchParams: (params: URLSearchParams) => void
  selectedSeries: SeriesCard | null
  setSelectedSeries: (series: SeriesCard | null) => void
  selectedSeason: number | null
  setSelectedSeason: (season: number | null) => void
  onClearSeriesState?: () => void
}

export function useLibrarySeriesSelection({
  libraryID,
  items,
  seriesEpisodeItems,
  isSeriesLibrary,
  isSeries,
  loading,
  seriesCards,
  searchParams,
  setSearchParams,
  selectedSeries,
  setSelectedSeries,
  selectedSeason,
  setSelectedSeason,
  onClearSeriesState,
}: UseLibrarySeriesSelectionOptions) {
  // 深链（首页最新条目 / 海报墙 ?series=<key>）指向的剧集不在已加载的
  // 一页卡片里时，需要单独按 key 解析；resolvingSeries 让页面在这段时间
  // 显示加载态，而不是先闪一下整个媒体库列表。
  const [resolvingSeries, setResolvingSeries] = useState(false)
  const [pendingSeriesKey, setPendingSeriesKey] = useState('')
  // 当前 URL 请求的剧集 key（非剧集库 / 加载中 / 没带参数时为空）。
  const requestedSeriesKey = !loading && isSeries ? (searchParams.get('series') ?? '') : ''

  const selectedEpisodes = useMemo(() => {
    const sourceItems = isSeriesLibrary ? seriesEpisodeItems : items
    if (!selectedSeries || sourceItems.length === 0) return []
    const eps = isSeriesLibrary
      ? sourceItems
      : sourceItems.filter((m) => getSeriesKey(m) === selectedSeries.key)
    const seasons = new Map<number, Media[]>()
    for (const ep of eps) {
      const specialSection = specialSectionForMedia(ep)
      const s = specialSection ??
        (ep.episode_num > 0 ? (ep.season_num ?? 0) : (ep.season_num || 1))
      if (!seasons.has(s)) seasons.set(s, [])
      seasons.get(s)!.push(ep)
    }
    for (const [, list] of seasons) {
      list.sort((a, b) => (a.episode_num || 0) - (b.episode_num || 0))
    }
    return Array.from(seasons.entries())
      .sort(([a], [b]) => seasonSortOrder(a) - seasonSortOrder(b))
      .map(([season, episodes]) => ({ season, episodes }))
  }, [isSeriesLibrary, selectedSeries, items, seriesEpisodeItems])

  const visibleEpisodes = useMemo(() => {
    if (selectedSeason == null) return selectedEpisodes[0]?.episodes ?? []
    return selectedEpisodes.find((s) => s.season === selectedSeason)?.episodes ?? []
  }, [selectedEpisodes, selectedSeason])

  const selectedSeriesEpisodes = useMemo(
    () => selectedEpisodes.flatMap((season: SeasonEpisodes) => season.episodes),
    [selectedEpisodes],
  )

  // 批量/整剧操作必须覆盖被折叠进 versions 的每一行, 否则只作用到代表行。
  const selectedSeriesAllEpisodes = useMemo(
    () => expandSeriesMediaVersions(selectedSeriesEpisodes),
    [selectedSeriesEpisodes],
  )

  const selectedSeriesMediaIDs = useMemo(
    () => selectedSeriesAllEpisodes.filter((ep) => !isTheatricalFeature(ep)).map((ep) => ep.id),
    [selectedSeriesAllEpisodes],
  )

  useEffect(() => {
    if (loading) return
    if (!isSeries) {
      setPendingSeriesKey('')
      setSelectedSeries(null)
      setSelectedSeason(null)
      return
    }

    if (!requestedSeriesKey) {
      setPendingSeriesKey('')
      setSelectedSeries(null)
      return
    }

    const next = seriesCards.find((card) => card.key === requestedSeriesKey)
    if (next) {
      setPendingSeriesKey('')
      setSelectedSeries(next)
      return
    }

    // 目标剧集尚未加载到（远程库分页 / 大库分页）：登记待解析 key。
    setPendingSeriesKey((prev) => (prev === requestedSeriesKey ? prev : requestedSeriesKey))
  }, [isSeries, loading, requestedSeriesKey, seriesCards, setSelectedSeason, setSelectedSeries])

  useEffect(() => {
    // pendingSeriesKey 必须仍然是 URL 上请求的 key：切换媒体库/清空参数
    // 时直接丢弃待解析状态，避免把别的库的剧集解析进来。
    if (!pendingSeriesKey || pendingSeriesKey !== requestedSeriesKey) {
      setResolvingSeries(false)
      return
    }
    let cancelled = false
    setResolvingSeries(true)
    resolveSeriesCardByKey(libraryID, pendingSeriesKey)
      .then((card) => {
        if (cancelled) return
        setResolvingSeries(false)
        setSelectedSeries(card)
      })
      .catch(() => {
        if (cancelled) return
        setResolvingSeries(false)
        setSelectedSeries(null)
      })
    return () => {
      cancelled = true
    }
  }, [libraryID, pendingSeriesKey, requestedSeriesKey, setSelectedSeries])

  useEffect(() => {
    if (!selectedSeries || selectedEpisodes.length === 0) {
      setSelectedSeason(null)
      return
    }
    if (selectedSeason == null || !selectedEpisodes.some((s) => s.season === selectedSeason)) {
      setSelectedSeason(selectedEpisodes[0].season)
    }
  }, [selectedSeries, selectedEpisodes, selectedSeason, setSelectedSeason])

  const handleSeriesClick = (card: SeriesCard) => {
    setSelectedSeries(card)
    const next = new URLSearchParams(searchParams)
    next.set('series', card.key)
    setSearchParams(next)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  const clearSelectedSeries = () => {
    setSelectedSeries(null)
    setSelectedSeason(null)
    onClearSeriesState?.()
    const next = new URLSearchParams(searchParams)
    next.delete('series')
    setSearchParams(next)
  }

  return {
    resolvingSeries,
    selectedEpisodes,
    visibleEpisodes,
    selectedSeriesEpisodes,
    selectedSeriesAllEpisodes,
    selectedSeriesMediaIDs,
    handleSeriesClick,
    clearSelectedSeries,
  }
}
