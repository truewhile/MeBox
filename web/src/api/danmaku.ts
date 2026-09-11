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
  match_mode?: 'hash' | 'filename' | 'search' | 'manual' | string
}

export interface DanmakuLoadedInfo {
  animeTitle?: string
  episodeTitle?: string
  episodeId?: number | string
  matchMode?: 'hash' | 'filename' | 'search' | 'manual' | string
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

  // config returns the renderer knobs so the player can initialize its
  // danmaku control panel without admin privileges.
  config: () =>
    api
      .get<{
        enabled: boolean
        source?: string
        opacity: string
        font_size: string
        area: string
        /** Per-user preference: merge the same episode's multiple sources. */
        merge_sources: boolean
      }>('/danmaku/config')
      .then((r) => r.data),

  // updateSettings persists per-user danmaku preferences (survives reload).
  updateSettings: (settings: { mergeSources: boolean }) =>
    api
      .put<{ merge_sources: boolean }>('/danmaku/settings', {
        merge_sources: settings.mergeSources,
      })
      .then((r) => r.data),
}