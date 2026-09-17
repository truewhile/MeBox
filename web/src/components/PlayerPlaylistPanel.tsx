import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, Cloud, Film, Layers, Play, Search, X } from 'lucide-react'

import type { Media } from '../types'
import { seasonLabel, seasonSortOrder, seriesTitleFromPath } from '../utils/groupSeries'
import {
  isStrmMedia,
  mediaVersionFileName,
  mediaVersionLabel,
  mediaVersionMatches,
  mediaVersionSourceLabel,
} from '../utils/mediaVersion'
import { PLAYER_DRAWER, PLAYER_ICON_BUTTON, PLAYER_PANEL_HEADER } from './playerTheme'

export type SeasonGroup = {
  season: number
  episodes: Media[]
}

type PlayerPlaylistPanelProps = {
  open: boolean
  onClose: () => void
  currentMediaId: string
  /** 当前正在播放媒体的全部版本（含自身）。多版本时面板顶部展示版本切换。 */
  currentVersions?: Media[]
  episodes: Media[]
  onSelectEpisode: (media: Media) => void
  /** 切换到同一集/同一条目的另一个版本。 */
  onSelectVersion?: (media: Media) => void
}

/**
 * 选集面板 —— 右侧整条抽屉，内容参考 B 站的番剧选集。
 *
 * 以前这里是「悬浮卡片 + 每集一行缩略图」，两三百集的库翻起来很累，卡片本身
 * 也和画面上别的浮层长得不一样。现在改成：整条贴住播放区右边缘的抽屉，剧集用
 * 编号宫格排（一屏能看到几十集），标题/时长放在悬停提示里；当前播放的那一格用
 * 强调色填充。
 *
 * 多版本（同一部片的 4K / 1080p / 云端直链等）依旧不丢：宫格右上角的小角标表示
 * 这一集有多个版本，点角标会在宫格上方展开版本列表；当前正在播放的条目的版本
 * 则常驻在抽屉顶部，随时可换。单条媒体（电影）没有剧集列表时，抽屉本身就是
 * 版本选择器。
 */
export function PlayerPlaylistPanel({
  open,
  onClose,
  currentMediaId,
  currentVersions = [],
  episodes,
  onSelectEpisode,
  onSelectVersion,
}: PlayerPlaylistPanelProps) {
  const [filterText, setFilterText] = useState('')
  /** 正在挑选版本的剧集 id；null = 宫格直接播放。 */
  const [versionsForEpisodeId, setVersionsForEpisodeId] = useState<string | null>(null)
  const activeItemRef = useRef<HTMLButtonElement | null>(null)

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

  // 当前播放所在季（播放中的可能是某个剧集行的非首个版本，需按版本组比对）
  const currentSeason = useMemo(() => {
    const found = episodes.find((e) => mediaVersionMatches(e, currentMediaId))
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

  // 换集/换季时收起版本列表：那张列表是针对上一集的，留着会让人误点。
  useEffect(() => {
    setVersionsForEpisodeId(null)
  }, [open, selectedSeason, currentMediaId])

  // 当面板打开时，自动平滑滚动到当前播放集的位置
  useEffect(() => {
    if (!open || versionsForEpisodeId) return
    const timer = setTimeout(() => {
      activeItemRef.current?.scrollIntoView({ block: 'center', behavior: 'smooth' })
    }, 100)
    return () => clearTimeout(timer)
  }, [open, selectedSeason, currentMediaId, versionsForEpisodeId])

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

  const versionsToSwitch = currentVersions.length > 1 ? currentVersions : []
  // 只有一集（电影/单条媒体）时，面板就是版本选择器，不再重复列出一行「剧集」。
  const hasEpisodeList = episodes.length > 1
  const versionsForEpisode = versionsForEpisodeId
    ? (listToDisplay.find((ep) => ep.id === versionsForEpisodeId)?.versions ?? [])
    : []
  const expandedEpisode = versionsForEpisodeId
    ? listToDisplay.find((ep) => ep.id === versionsForEpisodeId)
    : undefined

  return (
    <div
      onClick={(e) => e.stopPropagation()}
      // 舞台用 pointerdown/pointerup 判断「移动端轻触画面」，抽屉上的触摸不该参与。
      onPointerDown={(e) => e.stopPropagation()}
      onPointerUp={(e) => e.stopPropagation()}
      className={`absolute inset-y-0 right-0 z-30 ${PLAYER_DRAWER}`}
    >
      <div className={PLAYER_PANEL_HEADER}>
        <div className="flex min-w-0 items-center gap-2 text-sm font-semibold">
          <span className="truncate">{hasEpisodeList ? '选集' : '版本'}</span>
          <span className="shrink-0 font-mono text-[11px] font-normal text-white/45">
            {hasEpisodeList ? `${episodes.length} 集` : `${versionsToSwitch.length} 个版本`}
          </span>
        </div>
        <button onClick={onClose} className={PLAYER_ICON_BUTTON} title="关闭 (Esc)">
          <X size={16} />
        </button>
      </div>

      {/* 当前条目的版本切换：多版本时置顶，随时可换 */}
      {versionsToSwitch.length > 0 && (
        <div className="shrink-0 border-b border-white/10 px-3 py-2.5">
          <div className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium text-white/55">
            <Layers size={12} className="text-rose-400" />
            <span>正在播放的版本</span>
          </div>
          <div className="max-h-44 space-y-1 overflow-y-auto pr-0.5">
            {versionsToSwitch.map((version) => (
              <VersionOption
                key={version.id}
                version={version}
                active={mediaVersionMatches(version, currentMediaId)}
                onSelect={() => onSelectVersion?.(version)}
              />
            ))}
          </div>
        </div>
      )}

      {/* 某一集的版本列表：从宫格角标展开，选完即收起 */}
      {expandedEpisode && versionsForEpisode.length > 0 && (
        <div className="shrink-0 border-b border-white/10 bg-white/[0.03] px-3 py-2.5">
          <div className="mb-1.5 flex items-center justify-between gap-2">
            <div className="flex min-w-0 items-center gap-1.5 text-[11px] font-medium text-amber-200">
              <Layers size={12} className="shrink-0" />
              <span className="truncate">
                {expandedEpisode.episode_num > 0
                  ? `第 ${expandedEpisode.episode_num} 集 · 选择版本`
                  : '选择版本'}
              </span>
            </div>
            <button
              onClick={() => setVersionsForEpisodeId(null)}
              className="shrink-0 rounded p-0.5 text-white/45 transition hover:bg-white/10 hover:text-white"
              title="收起版本列表"
            >
              <X size={13} />
            </button>
          </div>
          <div className="max-h-44 space-y-1 overflow-y-auto pr-0.5">
            {versionsForEpisode.map((version) => (
              <VersionOption
                key={version.id}
                version={version}
                active={mediaVersionMatches(version, currentMediaId)}
                compact
                onSelect={() => {
                  onSelectVersion?.(version)
                  setVersionsForEpisodeId(null)
                }}
              />
            ))}
          </div>
        </div>
      )}

      {/* 季选择 Tabs（若有多季） */}
      {hasEpisodeList && seasonGroups.length > 1 && (
        <div className="no-scrollbar flex shrink-0 items-center gap-1.5 overflow-x-auto border-b border-white/10 px-3 py-2">
          {seasonGroups.map(({ season, episodes: sesEps }) => {
            const isSelected = selectedSeason === season
            const isPlayingThisSeason = sesEps.some((e) => mediaVersionMatches(e, currentMediaId))
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
      {hasEpisodeList && listToDisplay.length > 10 && (
        <div className="shrink-0 px-3 pb-1 pt-2.5">
          <div className="flex items-center gap-1.5 rounded-lg border border-white/10 bg-white/5 px-2.5 py-1.5 text-xs">
            <Search size={13} className="shrink-0 text-white/40" />
            <input
              type="text"
              value={filterText}
              onChange={(e) => setFilterText(e.target.value)}
              placeholder="搜索集数或标题…"
              className="w-full bg-transparent text-xs text-white outline-none placeholder:text-white/30"
            />
            {filterText && (
              <button onClick={() => setFilterText('')} className="text-white/40 hover:text-white">
                <X size={12} />
              </button>
            )}
          </div>
        </div>
      )}

      {/* 集数宫格 */}
      {hasEpisodeList ? (
        <div className="min-h-0 flex-1 overflow-y-auto p-3 select-none">
          {filteredEpisodes.length === 0 ? (
            <div className="py-10 text-center text-xs text-white/40">
              {filterText ? '未找到匹配的剧集' : '暂无剧集列表'}
            </div>
          ) : (
            <div className="grid grid-cols-5 gap-1.5 sm:grid-cols-6">
              {filteredEpisodes.map((ep) => {
                const isPlaying = mediaVersionMatches(ep, currentMediaId)
                const versions = ep.versions && ep.versions.length > 1 ? ep.versions : []
                const displayTitle = getEpisodeTitle(ep, listToDisplay)
                const durationText =
                  ep.duration_sec > 0 ? `${Math.round(ep.duration_sec / 60)} 分钟` : ''
                const cellTitle = [
                  ep.episode_num > 0 ? `第 ${ep.episode_num} 集` : '',
                  displayTitle,
                  durationText,
                  versions.length > 0 ? `${versions.length} 个版本` : '',
                ]
                  .filter(Boolean)
                  .join(' · ')

                return (
                  <div key={ep.id} className="relative">
                    <button
                      ref={isPlaying ? activeItemRef : null}
                      onClick={() => onSelectEpisode(ep)}
                      title={cellTitle}
                      className={`flex h-9 w-full items-center justify-center rounded-md text-xs font-medium tabular-nums transition ${
                        isPlaying
                          ? 'bg-rose-500 text-white shadow-[0_0_0_1px_rgba(255,255,255,0.2)_inset]'
                          : 'bg-white/[0.06] text-white/80 hover:bg-white/15 hover:text-white'
                      }`}
                    >
                      {isPlaying ? (
                        <Play size={12} className="fill-current" />
                      ) : ep.episode_num > 0 ? (
                        ep.episode_num
                      ) : (
                        <Film size={13} className="text-white/45" />
                      )}
                    </button>
                    {/* 有多个版本：点角标挑版本，点格子本身直接播放默认版本 */}
                    {versions.length > 0 && (
                      <button
                        onClick={(event) => {
                          event.stopPropagation()
                          setVersionsForEpisodeId((current) =>
                            current === ep.id ? null : ep.id,
                          )
                        }}
                        className={`absolute right-0 top-0 flex h-3.5 w-3.5 items-center justify-center rounded-bl-md rounded-tr-md text-[9px] font-semibold leading-none transition ${
                          versionsForEpisodeId === ep.id
                            ? 'bg-amber-300 text-black'
                            : 'bg-black/45 text-amber-200 hover:bg-black/70'
                        }`}
                        title={`${versions.length} 个版本，点这里选择`}
                      >
                        {versions.length}
                      </button>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>
      ) : (
        versionsToSwitch.length === 0 && (
          <div className="flex flex-1 items-center justify-center px-4 text-center text-xs text-white/40">
            暂无可切换的内容
          </div>
        )
      )}
    </div>
  )
}

/** 单个版本选项：主行是画质/容器/体积，次行是来源与文件名。 */
function VersionOption({
  version,
  active,
  compact = false,
  onSelect,
}: {
  version: Media
  active: boolean
  compact?: boolean
  onSelect: () => void
}) {
  const cloud = isStrmMedia(version)
  const label = mediaVersionLabel(version)
  const fileName = mediaVersionFileName(version)
  const sourceLabel = mediaVersionSourceLabel(version)
  // 没有探测数据时（网盘 STRM 常见）主行本身就是文件名，次行再重复一遍没有意义。
  const showFileName = fileName !== '' && !labelKey(label).includes(labelKey(fileName))
  const activeClass = 'border-rose-500/50 bg-rose-500/15 text-white'
  const idleClass =
    'border-white/5 bg-white/5 text-white/85 hover:border-white/15 hover:bg-white/10'

  return (
    <button
      type="button"
      disabled={active}
      onClick={onSelect}
      title={version.path || version.strm_url || undefined}
      className={`flex w-full items-start gap-2 rounded-lg border px-2.5 text-left transition disabled:cursor-default ${
        compact ? 'py-1.5' : 'py-2'
      } ${active ? activeClass : idleClass}`}
    >
      <span className="mt-0.5 shrink-0">
        {cloud ? (
          <Cloud size={13} className={active ? 'text-rose-300' : 'text-white/45'} />
        ) : (
          <Film size={13} className={active ? 'text-rose-300' : 'text-white/45'} />
        )}
      </span>
      <span className="min-w-0 flex-1">
        <span
          className={`block truncate text-xs font-medium ${active ? 'text-rose-200' : 'text-white/90'}`}
        >
          {label}
        </span>
        <span className="mt-0.5 block truncate text-[10px] text-white/45">
          {sourceLabel}
          {showFileName ? ` · ${fileName}` : ''}
        </span>
      </span>
      {active && <Check size={13} className="mt-0.5 shrink-0 text-rose-400" />}
    </button>
  )
}

/** 归一化后比较：忽略大小写与分隔符（. _ - 空格），用于判断两段文案是否只是重复。 */
function labelKey(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '')
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
