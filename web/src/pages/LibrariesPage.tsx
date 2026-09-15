import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { libraryAPI } from '../api/library'
import { toolsAPI } from '../api/tools'
import { openManageLibrariesDialog } from '../components/manageLibrariesDialog'
import { useEpisodeArtworkPreference } from '../hooks/useEpisodeArtworkPreference'
import { usePinnedLibraries } from '../hooks/usePinnedLibraries'
import { useAuthStore } from '../stores/auth'
import {
  LibrariesContent,
  LibrariesEmptyState,
  LibrariesHeader,
} from './LibrariesPageSections'
import type { LibraryPreview } from './librariesPageModel'
import type { Library } from '../types'
import type { SeriesCard } from '../utils/groupSeries'
import { fetchLibraries, invalidateLibraries, peekLibraries } from '../utils/libraryCache'
import { sortLibraryPreviews } from '../utils/pinnedLibraries'
import { partitionPreviewIDs } from '../utils/remoteEmby'

export function LibrariesPage() {
  const isAdmin = useAuthStore((state) => state.user?.role === 'admin')
  const [libraries, setLibraries] = useState<Library[]>([])
  const [libraryData, setLibraryData] = useState<Record<string, { cards: SeriesCard[]; total: number }>>({})
  const { pinnedIds, loading: pinnedLoading, togglePin } = usePinnedLibraries()
  const [loading, setLoading] = useState(true)
  const [repairing, setRepairing] = useState(false)
  const [repairEpisodeArtwork, setRepairEpisodeArtwork] = useEpisodeArtworkPreference()
  const [repairMsg, setRepairMsg] = useState('')

  // 缓存每个库已加载到的预览数量：入口网格只需要 2 张，横向货架需要 10 张。
  const fetchedPreviewLimitsRef = useRef<Map<string, number>>(new Map())
  const fetchingPreviewLimitsRef = useRef<Map<string, number>>(new Map())

  const fetchPreviews = useCallback(async (ids: string[], limit = 10) => {
    const targets = ids.filter(
      (id) =>
        (fetchedPreviewLimitsRef.current.get(id) ?? 0) < limit &&
        (fetchingPreviewLimitsRef.current.get(id) ?? 0) < limit,
    )
    if (targets.length === 0) return
    targets.forEach((id) => fetchingPreviewLimitsRef.current.set(id, limit))

    const batches = partitionPreviewIDs(targets)
    await Promise.allSettled(
      batches.map(async (batch) => {
        let loaded = false
        try {
          const rows = await libraryAPI.listPreviews(batch, limit)
          loaded = true
          const accepted = rows.filter(
            (row) => (fetchedPreviewLimitsRef.current.get(row.id) ?? 0) < limit,
          )
          accepted.forEach((row) => {
            fetchedPreviewLimitsRef.current.set(
              row.id,
              Math.max(fetchedPreviewLimitsRef.current.get(row.id) ?? 0, limit),
            )
          })
          setLibraryData((prev) => {
            const next = { ...prev }
            for (const row of accepted) {
              next[row.id] = {
                cards: row.cards ?? [],
                total: row.total ?? 0,
              }
            }
            return accepted.length > 0 ? next : prev
          })
        } catch {
          // 单个批次失败不影响其他批次。
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

  const loadLibraries = useCallback(async (options?: { force?: boolean }) => {
    if (options?.force) invalidateLibraries()
    // 会话缓存命中时先行渲染，避免每次进入都白等一轮请求
    const cached = peekLibraries()
    if (cached) {
      setLibraries(cached)
      setLoading(false)
    } else {
      setLoading(true)
    }
    try {
      const libs = await fetchLibraries()
      setLibraries(libs)
    } finally {
      setLoading(false)
    }
  }, [])

  async function handleRepairRescrape() {
    if (!isAdmin || repairing) return
    setRepairing(true)
    setRepairMsg('')
    try {
      await toolsAPI.repairAndRescrapeAll({ episode_images: repairEpisodeArtwork, refresh_matched: true })
      setRepairMsg('已开始全库修复+重刮，进度可在任务中查看。')
    } catch {
      setRepairMsg('启动失败，请稍后重试。')
    } finally {
      setRepairing(false)
    }
  }

  const handleManageLibraries = async () => {
    if (!isAdmin) return
    await openManageLibrariesDialog()
    await loadLibraries({ force: true })
  }

  useEffect(() => {
    loadLibraries().catch(() => undefined)
  }, [loadLibraries])

  const previews: LibraryPreview[] = useMemo(() => {
    return libraries.map((library) => {
      const data = libraryData[library.id]
      return {
        library,
        items: [],
        total: library.total ?? data?.total ?? 0,
        cards: data?.cards ?? [],
      }
    })
  }, [libraries, libraryData])

  const sortedPreviews = useMemo(() => sortLibraryPreviews(previews, pinnedIds), [previews, pinnedIds])

  const handleTogglePin = useCallback((libraryId: string) => {
    void togglePin(libraryId)
  }, [togglePin])

  const total = useMemo(() => previews.reduce((sum, preview) => sum + preview.total, 0), [previews])

  if (loading || pinnedLoading) {
    return <p className="px-2 py-8 text-sm text-sand-500">媒体库加载中…</p>
  }

  return (
    <div className="space-y-8">
      <LibrariesHeader
        previewCount={previews.length}
        total={total}
        isAdmin={isAdmin}
        repairMsg={repairMsg}
        repairEpisodeArtwork={repairEpisodeArtwork}
        repairing={repairing}
        onRepairEpisodeArtworkChange={setRepairEpisodeArtwork}
        onRepairRescrape={handleRepairRescrape}
        onManageLibraries={handleManageLibraries}
      />

      {previews.length === 0 ? (
        <LibrariesEmptyState isAdmin={isAdmin} />
      ) : (
        <LibrariesContent
          previews={sortedPreviews}
          pinnedIds={pinnedIds}
          onTogglePin={handleTogglePin}
          onNeedPreviews={fetchPreviews}
        />
      )}
    </div>
  )
}
