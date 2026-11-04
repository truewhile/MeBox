import type { HistoryItem, Media } from '../types'
import { formatEpisodeLabel } from '../utils/episodeNumber.ts'

/**
 * 媒体详情页的「继续播放」解析。
 *
 * 服务端存的是「(user, media) → 最近位置」的续播行。详情页要回答两个问题：
 * 1. 这部剧最新看到哪一集？（代表条目可能是 OVA/特别篇，不能直接用它自己的进度）
 * 2. 那一集看到第几秒？
 *
 * 因此这里拿到的是「整库最近 N 条历史」，在「本条目 + 全部剧集（含各自的版本）」
 * 这个集合里挑 watched_at 最新的一条。历史请求用 include_media=false，服务端
 * 不需要 join media，也不会为挂载条目逐条回源。
 */

/** 播放器只在「>2 秒且未看完」时才真正续播，详情页用同一阈值决定按钮文案。 */
export const RESUME_MIN_POSITION_MS = 2000

/** 详情页续播扫描的条数（服务端上限 500）。 */
export const RESUME_SCAN_LIMIT = 300

export interface MediaDetailResume {
  /** 实际要打开的媒体 id：命中的那条历史，可能是某集的某个版本。 */
  mediaId: string
  /** 用来显示集号/标题的条目（命中的那集；电影就是本体）。 */
  media: Media
  positionMs: number
  durationMs: number
  completed: boolean
}

export interface EpisodeProgress {
  positionMs: number
  durationMs: number
  completed: boolean
}

function versionIDs(media: Media | null | undefined): string[] {
  return (media?.versions ?? []).map((v) => v.id).filter(Boolean)
}

/** 「本条 + 各集 + 各集版本」的 id 集合：续播候选范围。 */
export function mediaDetailScopeIDs(media: Media | null, episodes: Media[]): Set<string> {
  const ids = new Set<string>()
  const add = (item: Media | null | undefined) => {
    if (!item?.id) return
    ids.add(item.id)
    for (const id of versionIDs(item)) ids.add(id)
  }
  add(media)
  for (const ep of episodes) add(ep)
  return ids
}

/** 命中的 history.media_id 属于哪个条目：集优先，命中的是版本时归到它那一集。 */
function ownerOf(mediaID: string, media: Media | null, episodes: Media[]): Media | null {
  for (const ep of episodes) {
    if (ep.id === mediaID) return ep
    if (versionIDs(ep).includes(mediaID)) return ep
  }
  if (media) {
    if (media.id === mediaID) return media
    if (versionIDs(media).includes(mediaID)) return media
  }
  return null
}

function watchedAtScore(row: HistoryItem): number {
  const at = Date.parse(row.watched_at)
  return Number.isFinite(at) ? at : 0
}

function newestRow(rows: HistoryItem[]): HistoryItem | null {
  let best: HistoryItem | null = null
  for (const row of rows) {
    if (!row || !row.media_id) continue
    if (best == null || watchedAtScore(row) > watchedAtScore(best)) best = row
  }
  return best
}

/**
 * 详情页的续播目标；没有可用历史时返回 null（按钮退回「立即播放」）。
 */
export function resolveMediaDetailResume(
  media: Media | null,
  episodes: Media[],
  history: HistoryItem[],
): MediaDetailResume | null {
  if (!media) return null
  const ids = mediaDetailScopeIDs(media, episodes)
  if (ids.size === 0) return null
  const rows = history.filter((row) => row?.media_id && ids.has(row.media_id) && row.position_ms > 0)
  const best = newestRow(rows)
  if (!best) return null
  return {
    mediaId: best.media_id,
    media: ownerOf(best.media_id, media, episodes) ?? media,
    positionMs: best.position_ms,
    durationMs: best.duration_ms,
    completed: Boolean(best.completed),
  }
}

/** 是否该显示「继续播放」（与播放器真正会续播的条件保持一致）。 */
export function canContinuePlaying(resume: MediaDetailResume | null): boolean {
  if (!resume) return false
  return !resume.completed && resume.positionMs > RESUME_MIN_POSITION_MS
}

/** 12:34 / 1:02:03；非法输入返回空串。 */
export function formatResumeClock(positionMs: number): string {
  if (!Number.isFinite(positionMs) || positionMs <= 0) return ''
  const totalSec = Math.floor(positionMs / 1000)
  const hours = Math.floor(totalSec / 3600)
  const minutes = Math.floor((totalSec % 3600) / 60)
  const seconds = totalSec % 60
  const pad = (value: number) => String(value).padStart(2, '0')
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${minutes}:${pad(seconds)}`
}

/** 「继续播放 · 第 3 集 · 12:34」；电影没有集号时只带位置。 */
export function continueButtonLabel(resume: MediaDetailResume): string {
  const parts = ['继续播放']
  const episode = formatEpisodeLabel(resume.media)
  if (episode) parts.push(episode)
  const clock = formatResumeClock(resume.positionMs)
  if (clock) parts.push(clock)
  return parts.join(' · ')
}

/** 每集（含版本）的续播进度，用于剧集卡片上的进度条。 */
export function episodeProgressById(
  episodes: Media[],
  history: HistoryItem[],
): Map<string, EpisodeProgress> {
  const map = new Map<string, EpisodeProgress>()
  if (episodes.length === 0 || history.length === 0) return map
  const byMediaID = new Map<string, HistoryItem>()
  for (const row of history) {
    if (row?.media_id && row.position_ms > 0) byMediaID.set(row.media_id, row)
  }
  for (const ep of episodes) {
    const rows: HistoryItem[] = []
    for (const id of [ep.id, ...versionIDs(ep)]) {
      const row = byMediaID.get(id)
      if (row) rows.push(row)
    }
    const best = newestRow(rows)
    if (!best) continue
    map.set(ep.id, {
      positionMs: best.position_ms,
      durationMs: best.duration_ms,
      completed: Boolean(best.completed),
    })
  }
  return map
}

/** 进度条宽度（0-100）。时长未知时按「看过」显示一小段，避免整条空着。 */
export function progressPercent(progress: EpisodeProgress): number {
  if (progress.completed) return 100
  if (progress.durationMs > 0) {
    const ratio = (progress.positionMs / progress.durationMs) * 100
    return Math.min(99, Math.max(2, ratio))
  }
  return 5
}
