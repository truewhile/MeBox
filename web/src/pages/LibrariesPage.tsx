import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { libraryAPI } from '../api/library'
import { toolsAPI } from '../api/tools'
import { openManageLibrariesDialog } from '../components/manageLibrariesDialog'
import { useEpisodeArtworkPreference } from '../hooks/useEpisodeArtworkPreference'
import { usePinnedLibraries } from '../hooks/usePinnedLibraries'
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

export function LibrariesPage() {
  const [libraries, setLibraries] = useState<Library[]>([])
  const [libraryData, setLibraryData] = useState<Record<string, { cards: SeriesCard[]; total: number }>>({})
  const { pinnedIds, loading: pinnedLoading, togglePin } = usePinnedLibraries()
  const [loading, setLoading] = useState(true)
  const [repairing, setRepairing] = useState(false)
  const [repairEpisodeArtwork, setRepairEpisodeArtwork] = useEpisodeArtworkPreference()
  const [repairMsg, setRepairMsg] = useState('')

  const fetchedLibIdsRef = useRef<Set<string>>(new Set())
  const fetchingRef = useRef<Set<string>>(new Set())

  const fetchPreviews = useCallback(async (ids: string[]) => {
    const targets = ids.filter((id) => !fetchedLibIdsRef.current.has(id) && !fetchingRef.current.has(id))
    if (targets.length === 0) return
    targets.forEach((id) => fetchingRef.current.add(id))

    try {
      const rows = await libraryAPI.listPreviews(targets, 10)
      setLibraryData((prev) => {
        const next = { ...prev }
        for (const row of rows) {
          next[row.id] = {
            cards: row.cards ?? [],
            total: row.total ?? 0,
          }
        }
        return next
      })
    } catch {
      // 容错
    } finally {
      targets.forEach((id) => {
        fetchedLibIdsRef.current.add(id)
        fetchingRef.current.delete(id)
      })
    }
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
      // 优先拉取入口卡片网格当前页（前 20 个库）的预览
      const topIds = libs.slice(0, 20).map((l) => l.id)
      void fetchPreviews(topIds)
    } finally {
      setLoading(false)
    }
  }, [fetchPreviews])

  async function handleRepairRescrape() {
    if (repairing) return
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
        total: data?.total ?? 0,
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
        repairMsg={repairMsg}
        repairEpisodeArtwork={repairEpisodeArtwork}
        repairing={repairing}
        onRepairEpisodeArtworkChange={setRepairEpisodeArtwork}
        onRepairRescrape={handleRepairRescrape}
        onManageLibraries={handleManageLibraries}
      />

      {previews.length === 0 ? (
        <LibrariesEmptyState />
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
