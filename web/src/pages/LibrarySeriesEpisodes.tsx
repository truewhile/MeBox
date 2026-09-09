import { Link } from 'react-router-dom'
import { Play, Search } from 'lucide-react'

import { imageURL } from '../api/client'
import { ExternalPlayerButton } from '../components/ExternalPlayerButton'
import type { Media } from '../types'
import {
  isTheatricalFeature,
  seasonLabel,
  seriesTitleFromPath,
  THEATRICAL_SEASON,
} from '../utils/groupSeries'
import { formatSize } from './libraryPageModel'

type SeasonGroup = {
  season: number
  episodes: Media[]
}

type LibrarySeriesEpisodesProps = {
  loading: boolean
  selectedEpisodes: SeasonGroup[]
  selectedSeason: number | null
  visibleEpisodes: Media[]
  playbackFrom: string
  isAdmin?: boolean
  onSeasonChange: (season: number) => void
  onManualScrape?: (media: Media) => void
}

export function LibrarySeriesEpisodes({
  loading,
  selectedEpisodes,
  selectedSeason,
  visibleEpisodes,
  playbackFrom,
  isAdmin = false,
  onSeasonChange,
  onManualScrape,
}: LibrarySeriesEpisodesProps) {
  if (loading) {
    return (
      <div className="rounded-2xl bg-white/75 p-6 text-center text-sm text-sand-500 shadow-soft">
        正在加载剧集…
      </div>
    )
  }

  const displaySeason = selectedSeason ?? selectedEpisodes[0]?.season ?? 1

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        {selectedEpisodes.map(({ season, episodes }) => (
          <button
            key={season}
            onClick={() => onSeasonChange(season)}
            className={
              'rounded-xl border px-4 py-2 text-sm font-semibold transition ' +
              (selectedSeason === season
                ? 'border-brand-300 bg-brand-50 text-brand-700'
                : 'border-sand-200 bg-white text-ink-100 hover:border-brand-200 hover:text-brand-600')
            }
          >
            {seasonLabel(season)} · {episodes.length} {season === THEATRICAL_SEASON ? '部' : '集'}
          </button>
        ))}
      </div>

      <div>
        <h3 className="mb-3 font-display text-lg font-semibold text-ink-600">
          {seasonLabel(displaySeason)}
        </h3>
        <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5">
          {visibleEpisodes.map((ep) => {
            const displayTitle = episodeDisplayTitle(ep, visibleEpisodes)
            const versionCount = ep.versions?.length ?? 1
            return (
              <div
                key={ep.id}
                className="group flex items-center justify-between gap-3 rounded-xl border border-sand-200 bg-white p-2.5 shadow-card transition-all hover:border-brand-300 hover:shadow-card-hover"
              >
                <Link to={`/play/${ep.id}`} state={{ from: playbackFrom }} className="flex min-w-0 flex-1 items-center gap-3">
                  <div
                    className="relative flex h-11 w-16 shrink-0 items-center justify-center overflow-hidden rounded-lg bg-brand-50/70 border border-sand-200/60"
                    title={displayTitle}
                  >
                    {ep.backdrop_url || ep.poster_url ? (
                      <img
                        src={imageURL(ep.backdrop_url || ep.poster_url || '', ep.updated_at)}
                        alt=""
                        className="h-full w-full object-cover transition-transform duration-300 group-hover:scale-105"
                        referrerPolicy="no-referrer"
                      />
                    ) : (
                      <span className="text-brand-600 font-bold text-sm">{ep.episode_num || '—'}</span>
                    )}
                    <div className="absolute inset-0 flex items-center justify-center bg-black/25 opacity-0 transition-opacity duration-200 group-hover:opacity-100">
                      <Play size={15} className="fill-white text-white drop-shadow-sm" />
                    </div>
                    {ep.episode_num > 0 && (
                      <span className="absolute bottom-0 right-0 rounded-tl bg-black/75 px-1 py-0.5 text-[9px] font-bold leading-none text-white backdrop-blur-[2px]">
                        {ep.episode_num}
                      </span>
                    )}
                  </div>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-ink-600 transition-colors group-hover:text-brand-600" title={displayTitle}>
                      {displayTitle}
                    </p>
                    <p className="text-xs text-sand-500 whitespace-nowrap">
                      {ep.duration_sec > 0
                        ? `${Math.floor(ep.duration_sec / 60)} 分钟`
                        : formatSize(ep.size_bytes)}
                      {versionCount > 1 ? ` · ${versionCount} 版本` : ''}
                    </p>
                  </div>
                </Link>
                <div className="flex shrink-0 items-center gap-1">
                  {isAdmin && onManualScrape && isTheatricalFeature(ep) && (
                    <button
                      type="button"
                      onClick={() => onManualScrape(ep)}
                      className="btn-outline !px-2 !py-1.5 text-[11px]"
                      title="手动匹配剧场版"
                    >
                      <Search size={12} />
                      匹配
                    </button>
                  )}
                  <ExternalPlayerButton mediaId={ep.id} label="外部" compact />
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </>
  )
}

function episodeDisplayTitle(ep: Media, siblings: Media[]): string {
  let mainTitle = ''
  const epTitle = ep.episode_title?.trim()
  if (epTitle && !looksLikeSeriesTitle(ep, epTitle, siblings)) {
    mainTitle = epTitle
  } else {
    const mediaTitle = ep.title?.trim()
    if (mediaTitle && !looksLikeSeriesTitle(ep, mediaTitle, siblings)) {
      mainTitle = mediaTitle
    }
  }

  if (ep.episode_num > 0) {
    if (!mainTitle) {
      return `第 ${ep.episode_num} 集`
    }
    const prefixRegex = new RegExp(`^(第\\s*0*${ep.episode_num}\\s*集|ep?\\.?\\s*0*${ep.episode_num}\\b)`, 'i')
    if (prefixRegex.test(mainTitle)) {
      return mainTitle
    }
    return `第 ${ep.episode_num} 集 · ${mainTitle}`
  }

  return mainTitle || '未命名'
}

function looksLikeSeriesTitle(ep: Media, title: string, siblings: Media[]): boolean {
  const normalized = normalizeEpisodeTitle(title)
  if (!normalized) return true
  if (ep.original_name && normalizeEpisodeTitle(ep.original_name) === normalized) return true
  const pathTitle = seriesTitleFromPath(ep.path)
  if (pathTitle && normalizeEpisodeTitle(pathTitle) === normalized) return true

  const siblingTitles = new Set(
    siblings
      .map((item) => normalizeEpisodeTitle(item.title))
      .filter(Boolean),
  )
  return siblingTitles.size === 1 && siblingTitles.has(normalized) && siblings.length > 1
}

function normalizeEpisodeTitle(value?: string): string {
  return (value ?? '')
    .toLowerCase()
    .replace(/\s*\((?:19|20)\d{2}\)\s*/g, ' ')
    .replace(/\s*\{(?:tmdb|tmdbid|douban|bangumi|bgm|thetvdb|tvdb)[\s:=#-]*[a-z0-9_-]+\}\s*/g, ' ')
    .replace(/[\s._-]+/g, ' ')
    .trim()
}
