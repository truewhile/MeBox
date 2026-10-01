import type { Media } from '../types'

export function mediaLibraryBackTarget(media: Media): string {
  const libraryID = media.display_library_id || media.library_id
  if (libraryID) {
    return `/library/${encodeURIComponent(libraryID)}`
  }
  return '/libraries'
}

export function mediaDetailScrapeMediaType(media: Media): string | undefined {
  return media.season_num > 0 || media.episode_num > 0 ? 'tv' : undefined
}
