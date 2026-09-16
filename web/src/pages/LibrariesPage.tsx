import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { libraryAPI } from '../api/library'
import { toolsAPI } from '../api/tools'
import { LibraryTagBar } from '../components/LibraryTagBar'
import { ManageLibraryTagsDialogView } from '../components/ManageLibraryTagsDialogView'
import { openManageLibrariesDialog } from '../components/manageLibrariesDialog'
import { useEpisodeArtworkPreference } from '../hooks/useEpisodeArtworkPreference'
import { useLibraryTags } from '../hooks/useLibraryTags'
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
import { ALL_TAG_ID, buildLibraryTagTabs, filterLibrariesByTag } from '../utils/libraryTags'
import type { LibraryTag } from '../utils/libraryTags'
import { sortLibraryPreviews } from '../utils/pinnedLibraries'
import { partitionPreviewIDs } from '../utils/remoteEmby'

export function LibrariesPage() {
  const isAdmin = useAuthStore((state) => state.user?.role === 'admin')
  const [libraries, setLibraries] = useState<Library[]>([])
  const [libraryData, setLibraryData] = useState<Record<string, { cards: SeriesCard[]; total: number }>>({})
  const { pinnedIds, loading: pinnedLoading, togglePin } = usePinnedLibraries()
  const libraryTags = useLibraryTags()
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

  const [libraryTagsOpen, setLibraryTagsOpen] = useState(false)
  const effectiveTagId = libraryTags.selectedTagId
  const tagTabs = useMemo(
    () => buildLibraryTagTabs(sortedPreviews.map((preview) => preview.library), libraryTags.tags),
    [sortedPreviews, libraryTags.tags],
  )
  const taggedPreviews = useMemo(
    () => filterPreviewsByTag(sortedPreviews, libraryTags.tags, effectiveTagId),
    [sortedPreviews, libraryTags.tags, effectiveTagId],
  )
  const taggedTotal = useMemo(
    () => taggedPreviews.reduce((sum, preview) => sum + preview.total, 0),
    [taggedPreviews],
  )

  if (loading || pinnedLoading) {
    return <p className="px-2 py-8 text-sm text-sand-500">媒体库加载中…</p>
  }

  return (
    <div className="space-y-8">
      <LibrariesHeader
        previewCount={taggedPreviews.length}
        total={taggedTotal}
        isAdmin={isAdmin}
        repairMsg={repairMsg}
        repairEpisodeArtwork={repairEpisodeArtwork}
        repairing={repairing}
        onRepairEpisodeArtworkChange={setRepairEpisodeArtwork}
        onRepairRescrape={handleRepairRescrape}
        onManageLibraries={handleManageLibraries}
      />

      <LibraryTagBar
        tabs={tagTabs}
        selectedTagId={effectiveTagId}
        onSelect={libraryTags.setSelectedTagId}
        onCreate={(name) => {
          void libraryTags.createTag(name)
        }}
        onManage={() => setLibraryTagsOpen(true)}
        busy={libraryTags.saving}
      />

      {libraryTagsOpen && (
        <ManageLibraryTagsDialogView
          tags={libraryTags.tags}
          libraries={libraries}
          saving={libraryTags.saving}
          onCreate={libraryTags.createTag}
          onRename={libraryTags.renameTag}
          onRemove={libraryTags.removeTag}
          onAssign={libraryTags.assignLibrary}
          onAssignBatch={libraryTags.assignLibraries}
          onClose={() => setLibraryTagsOpen(false)}
        />
      )}

      {previews.length === 0 ? (
        <LibrariesEmptyState isAdmin={isAdmin} />
      ) : taggedPreviews.length === 0 ? (
        <p className="rounded-3xl border border-dashed border-[var(--app-border)] bg-[var(--app-panel)] px-4 py-16 text-center text-sm text-[var(--app-muted)]">
          「{effectiveTagId}」标签下还没有媒体库，可在「管理标签」里把媒体库归入该标签。
        </p>
      ) : (
        <LibrariesContent
          previews={taggedPreviews}
          pinnedIds={pinnedIds}
          onTogglePin={handleTogglePin}
          onNeedPreviews={fetchPreviews}
        />
      )}
    </div>
  )
}

/** 按选中标签过滤媒体库预览（ALL_TAG_ID 时原样返回，保留置顶/手动排序）。 */
function filterPreviewsByTag(previews: LibraryPreview[], tags: LibraryTag[], tagId: string): LibraryPreview[] {
  if (!tagId || tagId === ALL_TAG_ID) return previews
  const ordered = filterLibrariesByTag(
    previews.map((preview) => ({ id: preview.library.id })),
    tags,
    tagId,
  )
  const byId = new Map(previews.map((preview) => [preview.library.id, preview]))
  const out: LibraryPreview[] = []
  for (const item of ordered) {
    const preview = byId.get(item.id)
    if (preview) out.push(preview)
  }
  return out
}
