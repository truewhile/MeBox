import type { Library, Media } from '../types'
import { artworkScore, groupSeries, type SeriesCard } from '../utils/groupSeries'

export type LibraryPreview = {
  library: Library
  items?: Media[]
  total: number
  cards: SeriesCard[]
}

export function isSeriesLibraryType(type?: string) {
  return type === 'tv' || type === 'anime' || type === 'variety'
}

export function latestLibraryCards(items: Media[]): SeriesCard[] {
  return groupSeries(items)
    .sort((a, b) => mediaTime(b.rep) - mediaTime(a.rep) || artworkScore(b.rep) - artworkScore(a.rep))
    .slice(0, 10)
}

export function mediaTime(media: Media): number {
  const releaseTime = Date.parse(media.release_date || '')
  if (releaseTime) return releaseTime
  if (media.year > 0) return Date.UTC(media.year, 11, 31)
  return Date.parse(media.updated_at || media.created_at || '') || 0
}

export function libraryArtworkItems(cards: SeriesCard[] = []): Array<{ src: string; version?: string }> {
  return [...cards]
    .sort((a, b) => artworkScore(b.rep) - artworkScore(a.rep) || mediaTime(b.rep) - mediaTime(a.rep))
    .map((card) => ({
      src: card.rep.poster_url || card.rep.backdrop_url || '',
      version: card.rep.updated_at,
    }))
    .filter((item) => Boolean(item.src))
    .slice(0, 2)
}

export function getLibraryArtworks(
  library: Library,
  cards: SeriesCard[] = [],
): Array<{ src: string; version?: string }> {
  if (library.cover_url) {
    return [{ src: library.cover_url, version: library.updated_at }]
  }
  return libraryArtworkItems(cards)
}
