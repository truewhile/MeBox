import type { Media } from './media'

export interface HistoryItem {
  id: string
  user_id: string
  media_id: string
  position_ms: number
  duration_ms: number
  watched_at: string
  completed: boolean
  media?: Media
}

export interface HistoryDailyStat {
  day: string
  watch_ms: number
  plays: number
}

export interface HistoryTypeStat {
  type: string
  watch_ms: number
  count: number
}

export interface HistoryStats {
  total: number
  completed: number
  watched_ms: number
  watched_hours: number
  last_watched?: string
  /** 正在看（未标记看完）的条目数。 */
  in_progress?: number
  /** 近 30 天中有观看记录的日子，未观看的日期不返回。 */
  daily?: HistoryDailyStat[]
  /** 按媒体库类型聚合的观看时长与条目数。 */
  by_library_type?: HistoryTypeStat[]
  /** 最近看过的若干条，含 media 详情用于渲染卡片。 */
  recent?: Array<{ history: HistoryItem; media?: Media }>
}
