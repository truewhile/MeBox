import { useCallback, useEffect, useMemo, useState, type Dispatch, type SetStateAction } from 'react'
import type { NavigateFunction } from 'react-router-dom'
import toast from 'react-hot-toast'

import { api } from '../api/client'
import { mediaAPI } from '../api/library'
import { playbackAPI } from '../api/playback'
import { confirmActionResult } from '../components/confirmAction'
import { useEpisodeArtworkPreference } from '../hooks/useEpisodeArtworkPreference'
import type { Media } from '../types'
import { seasonSortOrder } from '../utils/groupSeries'
import { mediaLibraryBackTarget } from './MediaDetailPageModel'

interface MediaDetailPageStateParams {
  id: string
  navigate: NavigateFunction
}

interface MediaDetailRefreshParams {
  id: string
  setMedia: Dispatch<SetStateAction<Media | null>>
  setFavourite: Dispatch<SetStateAction<boolean>>
  setLoading: Dispatch<SetStateAction<boolean>>
  setEpisodes: Dispatch<SetStateAction<Media[]>>
  setLoadingEpisodes: Dispatch<SetStateAction<boolean>>
}

interface MediaDetailActionsParams {
  media: Media | null
  scrapeEpisodeArtwork: boolean
  navigate: NavigateFunction
  refresh: () => Promise<void>
  setFavourite: Dispatch<SetStateAction<boolean>>
}

export function useMediaDetailPageState({ id, navigate }: MediaDetailPageStateParams) {
  const [media, setMedia] = useState<Media | null>(null)
  const [favourite, setFavourite] = useState(false)
  const [loading, setLoading] = useState(true)
  const [episodes, setEpisodes] = useState<Media[]>([])
  const [loadingEpisodes, setLoadingEpisodes] = useState(false)
  const [selectedSeason, setSelectedSeason] = useState<number | null>(null)

  const [manualScrapeOpen, setManualScrapeOpen] = useState(false)
  const [metadataEditOpen, setMetadataEditOpen] = useState(false)
  const [organizeOpen, setOrganizeOpen] = useState(false)
  const [scrapeEpisodeArtwork, setScrapeEpisodeArtwork] = useEpisodeArtworkPreference()

  const refresh = useMediaDetailRefresh({
    id,
    setMedia,
    setFavourite,
    setLoading,
    setEpisodes,
    setLoadingEpisodes,
  })
  const actions = useMediaDetailActions({
    media,
    scrapeEpisodeArtwork,
    navigate,
    refresh,
    setFavourite,
  })

  useEffect(() => {
    refresh().catch(() => undefined)
  }, [refresh])

  const seasonGroups = useMemo(() => {
    if (episodes.length === 0) return []
    const seasons = new Map<number, Media[]>()
    for (const ep of episodes) {
      const s = ep.episode_num > 0 ? (ep.season_num ?? 0) : (ep.season_num || 1)
      if (!seasons.has(s)) seasons.set(s, [])
      seasons.get(s)!.push(ep)
    }
    for (const [, list] of seasons) {
      list.sort((a, b) => (a.episode_num || 0) - (b.episode_num || 0))
    }
    return Array.from(seasons.entries())
      .sort(([a], [b]) => seasonSortOrder(a) - seasonSortOrder(b))
      .map(([season, list]) => ({ season, episodes: list }))
  }, [episodes])

  const visibleEpisodes = useMemo(() => {
    if (selectedSeason == null) return seasonGroups[0]?.episodes ?? []
    return seasonGroups.find((s) => s.season === selectedSeason)?.episodes ?? []
  }, [seasonGroups, selectedSeason])

  const hasEpisodes = useMemo(() => {
    if (episodes.length > 1) return true
    if (episodes.length === 1 && media) {
      // 只有单条时，如果明确是集（有 episode_num > 0）或 ID 与自身不同，则按剧集渲染
      return episodes[0].episode_num > 0 || episodes[0].id !== media.id
    }
    return false
  }, [episodes, media])

  const firstPlayableEpisode = useMemo(() => {
    if (episodes.length === 0) return null
    return (visibleEpisodes.length > 0 ? visibleEpisodes[0] : episodes[0]) || null
  }, [episodes, visibleEpisodes])

  const handleMetadataSaved = useCallback(async (next: Media) => {
    setMedia(next)
    await refresh()
  }, [refresh])

  return {
    media,
    favourite,
    loading,
    episodes,
    loadingEpisodes,
    seasonGroups,
    selectedSeason,
    visibleEpisodes,
    hasEpisodes,
    firstPlayableEpisode,
    setSelectedSeason,
    manualScrapeOpen,
    metadataEditOpen,
    organizeOpen,
    scrapeEpisodeArtwork,
    refresh,
    handleMetadataSaved,
    setManualScrapeOpen,
    setMetadataEditOpen,
    setOrganizeOpen,
    setScrapeEpisodeArtwork,
    ...actions,
  }
}

function useMediaDetailRefresh({
  id,
  setMedia,
  setFavourite,
  setLoading,
  setEpisodes,
  setLoadingEpisodes,
}: MediaDetailRefreshParams): () => Promise<void> {
  return useCallback(async () => {
    if (!id) return
    setLoading(true)
    setLoadingEpisodes(true)
    try {
      // 三个请求并行发出；详情一到就解锁整页渲染，收藏状态与分集列表
      // 到达后各自补齐（原先完全串行，首屏要排队等满三个往返）。
      const nextMediaPromise = mediaAPI.get(id)
      const favouritesPromise = playbackAPI.listFavourites().catch(() => [])
      const episodesPromise = mediaAPI
        .getEpisodes(id)
        .then((r) => r.items ?? [])
        .catch(() => [])

      const nextMedia = await nextMediaPromise
      setMedia(nextMedia)
      setLoading(false)

      const favourites = await favouritesPromise
      setFavourite(favourites.some((item) => item.id === nextMedia.id))

      const episodes = await episodesPromise
      setEpisodes(episodes)
    } finally {
      setLoading(false)
      setLoadingEpisodes(false)
    }
  }, [id, setFavourite, setLoading, setMedia, setEpisodes, setLoadingEpisodes])
}

function useMediaDetailActions({
  media,
  scrapeEpisodeArtwork,
  navigate,
  refresh,
  setFavourite,
}: MediaDetailActionsParams) {
  const goBack = useCallback(() => goBackFromMediaDetail(media, navigate), [media, navigate])
  const toggleFavourite = useCallback(
    () => toggleMediaFavourite(media, setFavourite),
    [media, setFavourite],
  )
  const rescrape = useCallback(
    () => rescrapeMedia(media, scrapeEpisodeArtwork, refresh),
    [media, refresh, scrapeEpisodeArtwork],
  )
  const reprobe = useCallback(() => reprobeMedia(media, refresh), [media, refresh])
  const exportNFO = useCallback(() => exportMediaNFO(media), [media])
  const deleteMedia = useCallback(
    () => deleteMediaFromLibrary(media, navigate),
    [media, navigate],
  )
  return { goBack, toggleFavourite, rescrape, reprobe, exportNFO, deleteMedia }
}

function goBackFromMediaDetail(media: Media | null, navigate: NavigateFunction, replace = false): void {
  if (!media) {
    navigate('/libraries')
    return
  }
  const backTarget = mediaLibraryBackTarget(media)
  navigate(backTarget || '/libraries', replace ? { replace: true } : undefined)
}

async function toggleMediaFavourite(
  media: Media | null,
  setFavourite: Dispatch<SetStateAction<boolean>>,
): Promise<void> {
  if (!media) return
  try {
    const state = await playbackAPI.toggleFavourite(media.id)
    setFavourite(state)
    toast.success(state ? '已加入我的收藏' : '已取消收藏')
  } catch (err: unknown) {
    toast.error(apiErrorMessage(err, '收藏操作失败'))
  }
}

async function rescrapeMedia(
  media: Media | null,
  scrapeEpisodeArtwork: boolean,
  refresh: () => Promise<void>,
): Promise<void> {
  if (!media) return
  try {
    await api.post(`/media/${media.id}/scrape`, {
      episode_images: scrapeEpisodeArtwork,
      refresh_matched: true,
      include_matched: true,
    })
  } catch (err: unknown) {
    toast.error(apiErrorMessage(err, '触发重新刮削失败'))
    return
  }
  toast.success('已触发重新刮削')
  await refresh().catch(() => undefined)
}

async function reprobeMedia(media: Media | null, refresh: () => Promise<void>): Promise<void> {
  if (!media) return
  try {
    const result = await api.post(`/media/${media.id}/probe`)
    if (result.data?.code === 0) toast.success('重新探测成功')
    else toast.error(result.data?.error || '探测失败')
    await refresh()
  } catch (err: unknown) {
    toast.error(apiErrorMessage(err, '探测失败，请检查 ffprobe 是否已安装'))
  }
}

async function exportMediaNFO(media: Media | null): Promise<void> {
  if (!media) return
  try {
    const result = await mediaAPI.exportNFO(media.id)
    toast.success(`NFO 已成功写入 ${result.path}`)
  } catch (err: unknown) {
    toast.error(apiErrorMessage(err, '导出失败'))
  }
}

async function deleteMediaFromLibrary(media: Media | null, navigate: NavigateFunction): Promise<void> {
  if (!media) return
  const result = await confirmActionResult({
    title: '删除媒体',
    message: `确定从媒体库删除「${media.title}」？\n默认仅移除索引记录，磁盘文件会保留。`,
    confirmText: '删除',
    checkboxLabel: '同时删除本地文件（含同名 NFO）',
  })
  if (!result.confirmed) return
  try {
    await mediaAPI.delete(media.id, { deleteFiles: result.checked })
  } catch (err: unknown) {
    toast.error(apiErrorMessage(err, '删除媒体失败'))
    return
  }
  toast.success(result.checked ? '已删除媒体及本地文件' : '已从媒体库删除')
  goBackFromMediaDetail(media, navigate, true)
}

function apiErrorMessage(err: unknown, fallback: string): string {
  return (err as { response?: { data?: { error?: string } } })?.response?.data?.error ?? fallback
}
