import type { Media } from '../types'

type EpisodeNumberSource = Pick<Media, 'episode_num' | 'episode_fraction'> | null | undefined

/**
 * 含小数的集号：S01E11.5 → 11.5。没有小数（绝大多数集）时就是 episode_num。
 *
 * 后端把整数集号和它的小数部分分开存（Emby 协议的 IndexNumber 只能是整数），
 * 网页端所有「显示集号 / 按集排序」的地方都应该用这个函数，而不是直接读
 * episode_num，否则 11 与 11.5 会显示、排序成同一集。
 */
export function episodeNumberValue(media: EpisodeNumberSource): number {
  const base = media?.episode_num ?? 0
  const fraction = media?.episode_fraction ?? 0
  if (!(fraction > 0 && fraction < 1)) return base
  return base + fraction
}

/** 显示用集号文本：11 → "11"，11.5 → "11.5"；没有集号时返回空串。 */
export function formatEpisodeNumber(media: EpisodeNumberSource): string {
  const value = episodeNumberValue(media)
  return value > 0 ? String(value) : ''
}

/** "第 11 集" / "第 11.5 集"；没有集号时返回空串。 */
export function formatEpisodeLabel(media: EpisodeNumberSource): string {
  const value = formatEpisodeNumber(media)
  return value ? `第 ${value} 集` : ''
}

/** 按 (季, 集, 小数部分) 升序比较，供剧集列表排序复用。 */
export function compareEpisodeOrder(a: Media, b: Media): number {
  const seasonDiff = (a.season_num ?? 0) - (b.season_num ?? 0)
  if (seasonDiff !== 0) return seasonDiff
  return episodeNumberValue(a) - episodeNumberValue(b)
}
