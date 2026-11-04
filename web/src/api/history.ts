import { api } from './client'
import type { HistoryItem, HistoryStats, Media } from '../types'

// historyAPI wraps /watch-history and /history. The two share storage on
// the backend; we treat /watch-history as the rich admin/dashboard
// surface and /history as the legacy resume-position write.
export const historyAPI = {
  list: (limit = 50) =>
    api
      .get<HistoryItem[]>('/watch-history', { params: { limit } })
      .then((r) => r.data),

  /**
   * 只要历史行、不要内嵌的 media。
   *
   * 媒体详情页解析「继续播放」只需要 media_id/position_ms；带上 media 会让
   * 300 条响应大一个数量级，而且服务端还得为每条去查 media（挂载条目逐条
   * 回源）。服务端已经支持 include_media=0 跳过 hydrate。
   */
  listRows: (limit = 300) =>
    api
      .get<HistoryItem[]>('/watch-history', { params: { limit, include_media: false } })
      .then((r) => r.data),

  stats: () => api.get<HistoryStats>('/watch-history/stats').then((r) => r.data),

  continueWatching: (limit = 10) =>
    api
      .get<{ history: HistoryItem; media: Media }[]>('/watch-history/continue', {
        params: { limit },
      })
      .then((r) => r.data),

  clear: (mediaID?: string, status?: 'completed' | 'incomplete') =>
    api
      .delete('/watch-history', {
        params: {
          ...(mediaID ? { media_id: mediaID } : {}),
          ...(status ? { status } : {}),
        },
      })
      .then((r) => r.data),

  remove: (id: string) => api.delete(`/watch-history/${id}`).then((r) => r.data),
}
