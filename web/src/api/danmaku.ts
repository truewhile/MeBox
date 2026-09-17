import { api } from './client'

// DanmakuEpisode / DanmakuAnime mirror the dandanplay search response. When
// multiple anime match, the backend returns them as candidates and the player
// asks the user which to load (disambiguation).
export interface DanmakuEpisode {
  episodeId: number
  episodeTitle: string
}

export interface DanmakuAnime {
  animeId: number
  animeTitle: string
  episodes: DanmakuEpisode[]
}

// DanmakuFetchResult mirrors the backend /api/danmaku/:id response: renderer
// knobs (resolved from runtime settings) plus the raw upstream payload. The
// dandanplay protocol delivers Bilibili-format XML or its own JSON shape
// ({comments:[{p,m,t}]}); source_type is sniffed from the body by the backend
// ("auto" when there is nothing to sniff). When several anime matched,
// candidates is non-empty and raw is empty.
export interface DanmakuFetchResult {
  enabled: boolean
  source_type: 'auto' | 'xml' | 'json'
  opacity: string
  font_size: string
  area: string
  raw?: string
  /** Number of sources merged into `raw`; absent/0 means not merged. */
  merged_sources?: number
  candidates?: DanmakuAnime[]
  /**
   * Same episode, other sources. Unlike `candidates` (which means "pick one
   * before anything loads"), `alternatives` arrives together with a loaded
   * library: the backend already auto-picked one and offers the rest so the
   * user can switch without re-searching. Aggregating sources such as LogVar
   * return several libraries for the same episode.
   */
  alternatives?: DanmakuAnime[]
  anime_title?: string
  episode_title?: string
  episode_id?: number
  match_mode?: 'hash' | 'filename' | 'metadata' | 'search' | 'manual' | string
}

export interface DanmakuLoadedInfo {
  animeTitle?: string
  episodeTitle?: string
  episodeId?: number | string
  matchMode?: 'hash' | 'filename' | 'metadata' | 'search' | 'manual' | string
  totalCount: number
  sourceType?: 'auto' | 'xml' | 'json'
  /** Number of sources merged into the loaded comments (0 = not merged). */
  mergedSources?: number
}

export type DanmakuFetchOptions = {
  /** Overrides the media-derived search keyword (manual search). */
  kw?: string
  /** Forces a specific danmaku library chosen by the user. */
  episodeId?: number | string
}

export interface DanmakuConfig {
  enabled: boolean
  source?: string
  app_id?: string
  app_key_configured: boolean
  opacity: string
  font_size: string
  area: string
  volume: number
  playback_rate: number
  /** 当前用户是否已经看过 VR 全景播放的首次操作说明（按用户存储）。 */
  vr360_guide_seen: boolean
  /** Per-user preference: merge the same episode's multiple sources. */
  merge_sources: boolean
}

export interface DanmakuSettingsPatch {
  enabled?: boolean
  source?: string
  app_id?: string
  /** 留空不会修改密钥；清空请使用 clear_app_key。 */
  app_key?: string
  clear_app_key?: boolean
  opacity?: number
  font_size?: number
  area?: number
  merge_sources?: boolean
  volume?: number
  playback_rate?: number
  /** 看过 VR 操作说明后置为 true，之后不再弹出。 */
  vr360_guide_seen?: boolean
}

// danmakuAPI fetches danmaku comments for a media item. The backend resolves
// the configured dandanplay source by the video's name (search for episode,
// then fetch its comment library) and returns the raw Bilibili-format XML;
// parsing into comment objects happens client-side in utils/parseDanmaku.
export const danmakuAPI = {
  fetch: (mediaId: string, options: DanmakuFetchOptions = {}) =>
    api
      .get<DanmakuFetchResult>(`/danmaku/${encodeURIComponent(mediaId)}`, {
        params: { kw: options.kw || undefined, episodeId: options.episodeId || undefined },
        timeout: 20_000,
      })
      .then((r) => r.data),

  // config returns the current user's player volume and danmaku preferences.
  config: () =>
    api.get<DanmakuConfig>('/danmaku/config').then((r) => r.data),

  // updateSettings persists a partial per-user player/danmaku settings patch.
  updateSettings: (settings: DanmakuSettingsPatch) =>
    api.put<DanmakuConfig>('/danmaku/settings', settings).then((r) => r.data),
}
