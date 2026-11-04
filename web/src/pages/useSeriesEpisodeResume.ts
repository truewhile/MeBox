import { useEffect, useMemo, useState } from 'react'

import { historyAPI } from '../api/history'
import type { HistoryItem, Media } from '../types'
import {
  episodeProgressById,
  resolveMediaDetailResume,
  RESUME_SCAN_LIMIT,
  type EpisodeProgress,
  type MediaDetailResume,
} from './mediaDetailResumeModel'

interface SeriesEpisodeResume {
  resume: MediaDetailResume | null
  progressById: Map<string, EpisodeProgress>
}

/**
 * 剧集面板（媒体库抽屉 / 媒体详情）用的续播数据。
 *
 * 代表条目可能是 OVA/特别篇，因此续播目标必须在「整剧所有集」里挑最近看过
 * 的一条；打开抽屉时扫一次整库最近历史即可，只取历史行（include_media=false）。
 */
export function useSeriesEpisodeResume(
  seriesID: string,
  rep: Media | null,
  episodes: Media[],
): SeriesEpisodeResume {
  const [history, setHistory] = useState<HistoryItem[]>([])

  useEffect(() => {
    if (!seriesID) {
      setHistory([])
      return
    }
    let cancelled = false
    historyAPI
      .listRows(RESUME_SCAN_LIMIT)
      .then((rows) => {
        if (!cancelled) setHistory(rows)
      })
      .catch(() => {
        if (!cancelled) setHistory([])
      })
    return () => {
      cancelled = true
    }
  }, [seriesID])

  const resume = useMemo(
    () => resolveMediaDetailResume(rep, episodes, history),
    [episodes, history, rep],
  )
  const progressById = useMemo(() => episodeProgressById(episodes, history), [episodes, history])
  return { resume, progressById }
}
