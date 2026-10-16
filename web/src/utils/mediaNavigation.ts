import type { Media } from '../types'
import { isRemoteEmbyID } from './remoteEmby'
import { getSeriesKey, isEpisodeLike, pathLooksEpisodic } from './groupSeries'

export function mediaSearchLink(query: string, page = 1): string {
  const params = new URLSearchParams({ q: query.trim() })
  if (page > 1) params.set('page', String(page))
  return `/search?${params}`
}

export function mediaSearchReturnPath(state: unknown): string | undefined {
  if (!state || typeof state !== 'object' || !('from' in state)) return undefined
  return typeof state.from === 'string' && state.from.startsWith('/search?') ? state.from : undefined
}

function pathLooksLikeMovie(media: Media): boolean {
  const path = (media.path || media.display_library_path || media.library_path || '').toLowerCase()
  return /[\\/](?:movie|movies|film|films|电影)[\\/]/i.test(path)
}

function isRemoteEmbyLibrarySeries(media: Media): boolean {
  if (!isRemoteEmbyID(media.id) || isEpisodeLike(media)) return false
  const libraryID = media.display_library_id || media.library_id
  if (!libraryID) return false
  return !pathLooksLikeMovie(media)
}

/** Whether opening the library series detail view is a better target than /media/:id. */
export function prefersLibrarySeriesView(media: Media): boolean {
  if (isEpisodeLike(media)) return true
  if (pathLooksEpisodic(media)) return true
  if (media.series_id) return true
  if (isRemoteEmbyLibrarySeries(media)) return true
  return false
}

/** Series key used by /library/:id?series=... for both local and remote libraries. */
export function resolveLibrarySeriesKey(media: Media): string {
  if (isRemoteEmbyID(media.id) && !isEpisodeLike(media)) {
    return media.id
  }
  return getSeriesKey(media)
}

/** Best library URL for a media item, or null when /media/:id is preferred. */
export function mediaLibraryTarget(media: Media): string | null {
  const libraryID = media.display_library_id || media.library_id
  if (!libraryID || !prefersLibrarySeriesView(media)) return null

  const seriesKey = resolveLibrarySeriesKey(media)
  const base = `/library/${encodeURIComponent(libraryID)}`
  return seriesKey ? `${base}?series=${encodeURIComponent(seriesKey)}` : base
}

/** Link target for favourites / cards that should open the library series view when possible. */
export function favouriteMediaLink(media: Media): string {
  return mediaLibraryTarget(media) ?? `/media/${media.id}`
}
