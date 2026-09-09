import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, Film, ListVideo, Play, Search, X } from 'lucide-react'

import { imageURL } from '../api/client'
import type { Media } from '../types'
import { seasonLabel, seasonSortOrder, seriesTitleFromPath } from '../utils/groupSeries'

export type SeasonGroup = {
  season: number
  episodes: Media[]
}

type PlayerPlaylistPanelProps = {
  open: boolean
  onClose: () => void
  currentMediaId: string
  episodes: Media[]
  onSelectEpisode: (media: Media) => void
}

export function PlayerPlaylistPanel({
  open,
  onClose,
  currentMediaId,
  episodes,
  onSelectEpisode,
}: PlayerPlaylistPanelProps) {
  const [filterText, setFilterText] = useState('')
  const activeItemRef = useRef<HTMLDivElement | null>(null)
  const listContainerRef = useRef<HTMLDivElement | null>(null)

  // 按季分组
  const seasonGroups = useMemo<SeasonGroup[]>(() => {
    if (!episodes || episodes.length === 0) return []
    const seasonsMap = new Map<number, Media[]>()
    for (const ep of episodes) {
      const s = ep.episode_num > 0 ? (ep.season_num ?? 0) : (ep.season_num || 1)
      if (!seasonsMap.has(s)) seasonsMap.set(s, [])
      seasonsMap.get(s)!.push(ep)
    }
    for (const [, list] of seasonsMap) {
      list.sort((a, b) => (a.episode_num || 0) - (b.episode_num || 0))
    }
    return Array.from(seasonsMap.entries())
      .sort(([a], [b]) => seasonSortOrder(a) - seasonSortOrder(b))
      .map(([season, list]) => ({ season, episodes: list }))
  }, [episodes])

  // 当前播放所在季
  const currentSeason = useMemo(() => {
    const found = episodes.find((e) => e.id === currentMediaId)
    if (!found) return seasonGroups[0]?.season ?? 1
    return found.episode_num > 0 ? (found.season_num ?? 0) : (found.season_num || 1)
  }, [episodes, currentMediaId, seasonGroups])

  const [selectedSeason, setSelectedSeason] = useState<number>(currentSeason)

  // 当当前播放媒体改变或打开面板时，默认选中当前媒体所在的季
  useEffect(() => {
    if (open) {
      setSelectedSeason(currentSeason)
    }
  }, [open, currentSeason])

  // 当面板打开时，自动平滑滚动到当前播放集的位置
  useEffect(() => {
    if (open && activeItemRef.current) {
      const timer = setTimeout(() => {
        activeItemRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
      }, 100)
      return () => clearTimeout(timer)
    }
  }, [open, selectedSeason, currentMediaId])

  if (!open) return null

  const currentGroup = seasonGroups.find((g) => g.season === selectedSeason) ?? seasonGroups[0]
  const listToDisplay = currentGroup ? currentGroup.episodes : episodes

  const filteredEpisodes = filterText.trim()
    ? listToDisplay.filter((ep) => {
        const query = filterText.trim().toLowerCase()
        const title = (ep.episode_title || ep.title || '').toLowerCase()
        const epNum = String(ep.episode_num)
        return title.includes(query) || epNum === query || `e${epNum}`.includes(query) || `第${epNum}集`.includes(query)
      })
    : listToDisplay

  return (
    <div
      onClick={(e) => e.stopPropagation()}
      className="absolute inset-x-3 top-12 bottom-3 z-30 flex flex-col overflow-hidden rounded-2xl border border-white/15 bg-black/85 text-white shadow-2xl backdrop-blur-md sm:inset-x-auto sm:right-4 sm:top-16 sm:bottom-20 sm:w-96"
    >
      {/* 头部 */}
      <div className="flex items-center justify-between border-b border-white/10 px-4 py-3 shrink-0">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <ListVideo size={17} className="text-rose-400" />
          <span>选集列表</span>
          <span className="font-mono text-xs font-normal text-white/50">
            ({episodes.length} 集)
          </span>
        </div>
        <button
          onClick={onClose}
          className="rounded-full p-1 text-white/60 transition hover:bg-white/10 hover:text-white"
          title="关闭"
        >
          <X size={16} />
        </button>
      </div>

      {/* 季选择 Tabs（若有多季） */}
      {seasonGroups.length > 1 && (
        <div className="flex items-center gap-1.5 border-b border-white/10 px-3 py-2 shrink-0 overflow-x-auto no-scrollbar">
          {seasonGroups.map(({ season, episodes: sesEps }) => {
            const isSelected = selectedSeason === season
            const isPlayingThisSeason = sesEps.some((e) => e.id === currentMediaId)
            return (
              <button
                key={season}
                onClick={() => {
                  setSelectedSeason(season)
                  setFilterText('')
                }}
                className={`relative flex shrink-0 items-center gap-1 rounded-lg px-2.5 py-1 text-xs font-medium transition ${
                  isSelected
                    ? 'bg-rose-500 text-white'
                    : 'bg-white/5 text-white/70 hover:bg-white/10 hover:text-white'
                }`}
              >
                <span>{seasonLabel(season)}</span>
                <span className="text-[10px] opacity-75">({sesEps.length})</span>
                {isPlayingThisSeason && !isSelected && (
                  <span className="h-1.5 w-1.5 rounded-full bg-rose-400" />
                )}
              </button>
            )
          })}
        </div>
      )}

      {/* 搜索/过滤单集（当单集数量较多时） */}
      {listToDisplay.length > 10 && (
        <div className="px-3 pt-2.5 pb-1.5 shrink-0">
          <div className="flex items-center gap-1.5 rounded-lg border border-white/10 bg-white/5 px-2.5 py-1 text-xs">
            <Search size={13} className="text-white/40 shrink-0" />
            <input
              type="text"
              value={filterText}
              onChange={(e) => setFilterText(e.target.value)}
              placeholder="搜索集数或标题…"
              className="w-full bg-transparent outline-none placeholder:text-white/30 text-white text-xs"
            />
            {filterText && (
              <button
                onClick={() => setFilterText('')}
                className="text-white/40 hover:text-white"
              >
                <X size={12} />
              </button>
            )}
          </div>
        </div>
      )}

      {/* 集数列表 */}
      <div
        ref={listContainerRef}
        className="flex-1 overflow-y-auto p-2.5 space-y-1.5 pr-2 select-none"
      >
        {filteredEpisodes.length === 0 ? (
          <div className="py-8 text-center text-xs text-white/40">
            {filterText ? '未找到匹配的剧集' : '暂无剧集列表'}
          </div>
        ) : (
          filteredEpisodes.map((ep) => {
            const isPlaying = ep.id === currentMediaId
            const displayTitle = getEpisodeTitle(ep, listToDisplay)
            const durationText =
              ep.duration_sec > 0 ? `${Math.floor(ep.duration_sec / 60)} 分钟` : ''

            return (
              <div
                key={ep.id}
                ref={isPlaying ? activeItemRef : null}
                onClick={() => onSelectEpisode(ep)}
                className={`group flex cursor-pointer items-center gap-2.5 rounded-xl p-2 transition border ${
                  isPlaying
                    ? 'border-rose-500/60 bg-rose-500/20 text-white'
                    : 'border-white/5 bg-white/5 hover:border-white/20 hover:bg-white/10 text-white/85'
                }`}
              >
                {/* 封面/集号 */}
                <div className="relative flex h-11 w-16 shrink-0 items-center justify-center overflow-hidden rounded-lg bg-white/10 text-xs font-semibold">
                  {ep.backdrop_url || ep.poster_url ? (
                    <img
                      src={imageURL(ep.backdrop_url || ep.poster_url || '', ep.updated_at)}
                      alt=""
                      className="h-full w-full object-cover"
                      referrerPolicy="no-referrer"
                    />
                  ) : (
                    <Film size={16} className="text-white/40" />
                  )}

                  {/* 正在播放动效 / 集数徽标 */}
                  {isPlaying ? (
                    <div className="absolute inset-0 flex items-center justify-center bg-black/60 backdrop-blur-xs">
                      <div className="flex items-end gap-0.5 h-3">
                        <span className="w-0.5 bg-rose-400 animate-pulse h-full" />
                        <span className="w-0.5 bg-rose-400 animate-pulse h-2" />
                        <span className="w-0.5 bg-rose-400 animate-pulse h-3" />
                      </div>
                    </div>
                  ) : (
                    <div className="absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 group-hover:opacity-100 transition-opacity">
                      <Play size={14} className="text-white fill-white" />
                    </div>
                  )}

                  {/* 角标显示集数 */}
                  <span className="absolute bottom-0.5 right-1 rounded bg-black/75 px-1 py-0.2 text-[9px] font-mono text-white/90">
                    {ep.episode_num > 0 ? `${ep.episode_num}` : '—'}
                  </span>
                </div>

                {/* 标题 & 时长 */}
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <p
                      className={`truncate text-xs font-medium ${
                        isPlaying ? 'text-rose-300 font-semibold' : 'group-hover:text-white'
                      }`}
                    >
                      {displayTitle}
                    </p>
                  </div>
                  <div className="flex items-center gap-2 mt-0.5 text-[10px] text-white/50">
                    {ep.episode_num > 0 && (
                      <span className="font-mono">第 {ep.episode_num} 集</span>
                    )}
                    {durationText && <span>{durationText}</span>}
                  </div>
                </div>

                {isPlaying && (
                  <div className="shrink-0 flex items-center gap-1 text-[11px] font-medium text-rose-400 px-1">
                    <Check size={13} />
                  </div>
                )}
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}

function getEpisodeTitle(ep: Media, siblings: Media[]): string {
  const title = ep.episode_title?.trim()
  if (title && !looksLikeSeriesTitle(ep, title, siblings)) {
    return title
  }

  const mediaTitle = ep.title?.trim()
  if (mediaTitle && !looksLikeSeriesTitle(ep, mediaTitle, siblings)) {
    return mediaTitle
  }

  return ep.episode_num > 0 ? `第 ${ep.episode_num} 集` : mediaTitle || title || '未命名'
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
