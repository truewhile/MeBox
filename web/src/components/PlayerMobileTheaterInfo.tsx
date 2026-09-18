import { useEffect, useRef } from 'react'
import type { ReactNode } from 'react'
import { ChevronDown, Layers, ListVideo } from 'lucide-react'

import type { Media } from '../types'
import { mediaVersionLabel, mediaVersionMatches } from '../utils/mediaVersion'

// PlayerMobileTheaterInfo — 竖屏剧场模式下视频下方的可滚动内容区。
//
// 竖屏手机不再用全屏居中黑边布局：视频贴顶按 16:9 自适应高度，
// 下面这块区域接管标题、选集、版本切换和简介，信息密度与
// 主流手机视频应用保持一致。桌面端与横屏手机不渲染它。
//
// 选集刻意放在这里而不是弹层里：从操作栏点「选集」只做「展开 + 滚到这里」，
// 播放画面始终可见，不会像浮层那样盖住正在看的内容。

type PlayerMobileTheaterInfoProps = {
  media: Media | null
  title: string
  subtitle?: string
  playbackModeLabel?: string
  qualityLabel?: string
  episodes: Media[]
  currentMediaId: string
  currentEpisodeIndex: number
  currentVersions?: Media[]
  onSelectEpisode: (media: Media) => void
  onSelectVersion?: (media: Media) => void
  /** 选集列表是否展开（展开后由 playlistPanel 接管，提供季/搜索/版本）。 */
  playlistOpen: boolean
  onTogglePlaylist: () => void
  /** 展开状态下的选集内容（复用播放器的选集面板，内嵌渲染）。 */
  playlistPanel?: ReactNode
  /**
   * 每次从操作栏点「选集」都会 +1：即使列表已经展开，也要把这块滚回视野内。
   */
  revealToken?: number
}

function episodeShortLabel(ep: Media): string {
  if (ep.episode_num > 0) return `${ep.episode_num}`
  const title = ep.episode_title?.trim() || ep.title?.trim() || ''
  return title ? title.slice(0, 4) : '·'
}

function episodeFullLabel(ep: Media): string {
  if (ep.episode_num > 0) {
    const title = ep.episode_title?.trim()
    return title && title !== ep.title?.trim() ? `第 ${ep.episode_num} 集 · ${title}` : `第 ${ep.episode_num} 集`
  }
  return ep.episode_title?.trim() || ep.title?.trim() || '未命名'
}

export function PlayerMobileTheaterInfo({
  media,
  title,
  subtitle,
  playbackModeLabel,
  qualityLabel,
  episodes,
  currentMediaId,
  currentEpisodeIndex,
  currentVersions = [],
  onSelectEpisode,
  onSelectVersion,
  playlistOpen,
  onTogglePlaylist,
  playlistPanel,
  revealToken = 0,
}: PlayerMobileTheaterInfoProps) {
  const episodeSectionRef = useRef<HTMLElement | null>(null)
  const hasEpisodeList = episodes.length > 1
  const versionsToSwitch = currentVersions.length > 1 ? currentVersions : []
  const overview = media?.overview?.trim() || ''

  // 从操作栏点「选集」：把选集区滚回视野内，用户不用自己往上翻。
  useEffect(() => {
    if (!revealToken) return
    episodeSectionRef.current?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  }, [revealToken])

  return (
    <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain bg-black px-4 pb-8 pt-3 text-white">
      <h1 className="text-[15px] font-semibold leading-snug text-white/95">{title || '加载中…'}</h1>
      {subtitle ? <p className="mt-1 text-xs text-white/55">{subtitle}</p> : null}
      {(playbackModeLabel || qualityLabel) && (
        <div className="mt-2 flex flex-wrap items-center gap-1.5">
          {qualityLabel ? (
            <span className="rounded-md bg-white/10 px-2 py-0.5 text-[11px] font-medium text-white/80">
              {qualityLabel}
            </span>
          ) : null}
          {playbackModeLabel ? (
            <span className="rounded-md bg-white/10 px-2 py-0.5 text-[11px] text-white/60">
              {playbackModeLabel}
            </span>
          ) : null}
        </div>
      )}

      {hasEpisodeList ? (
        <section ref={episodeSectionRef} className="mt-4 scroll-mt-2">
          <div className="mb-2 flex items-center justify-between">
            <h2 className="flex items-center gap-1.5 text-[13px] font-semibold text-white/90">
              <ListVideo size={14} className="text-rose-400" />
              选集
              <span className="font-mono text-[11px] font-normal text-white/45">
                {currentEpisodeIndex >= 0
                  ? `${currentEpisodeIndex + 1}/${episodes.length}`
                  : `${episodes.length} 集`}
              </span>
            </h2>
            <button
              type="button"
              onClick={onTogglePlaylist}
              className="flex min-h-8 items-center gap-1 rounded-md px-2 text-[11px] text-white/60 transition hover:bg-white/10 hover:text-white"
            >
              {playlistOpen ? '收起' : '全部'}
              <ChevronDown
                size={13}
                className={`transition-transform ${playlistOpen ? 'rotate-180' : ''}`}
              />
            </button>
          </div>

          {playlistOpen && playlistPanel ? (
            playlistPanel
          ) : (
            <div className="scrollbar-hide -mx-4 flex gap-1.5 overflow-x-auto px-4 pb-1">
              {episodes.map((ep) => {
                const active = mediaVersionMatches(ep, currentMediaId)
                return (
                  <button
                    key={ep.id}
                    type="button"
                    onClick={() => onSelectEpisode(ep)}
                    title={episodeFullLabel(ep)}
                    className={`flex h-10 min-w-12 shrink-0 items-center justify-center rounded-lg px-2 text-xs font-medium tabular-nums transition ${
                      active
                        ? 'bg-rose-500 text-white'
                        : 'bg-white/[0.07] text-white/80 active:bg-white/15'
                    }`}
                  >
                    {episodeShortLabel(ep)}
                  </button>
                )
              })}
            </div>
          )}
        </section>
      ) : null}

      {/* 多版本切换：展开选集后由选集面板顶部的版本区接管，这里不重复一份 */}
      {versionsToSwitch.length > 0 && !playlistOpen ? (
        <section className="mt-4">
          <h2 className="mb-2 flex items-center gap-1.5 text-[13px] font-semibold text-white/90">
            <Layers size={13} className="text-rose-400" />
            版本
            <span className="font-mono text-[11px] font-normal text-white/45">
              {versionsToSwitch.length} 个
            </span>
          </h2>
          <div className="space-y-1.5">
            {versionsToSwitch.map((version) => {
              const active = mediaVersionMatches(version, currentMediaId)
              return (
                <button
                  key={version.id}
                  type="button"
                  disabled={active}
                  onClick={() => onSelectVersion?.(version)}
                  className={`flex w-full items-center gap-2 rounded-lg border px-2.5 py-2 text-left transition disabled:cursor-default ${
                    active
                      ? 'border-rose-500/50 bg-rose-500/15 text-white'
                      : 'border-white/5 bg-white/5 text-white/85 active:bg-white/10'
                  }`}
                >
                  <span className="min-w-0 flex-1 truncate text-xs font-medium">
                    {mediaVersionLabel(version)}
                  </span>
                  {active ? <span className="shrink-0 text-[10px] text-rose-300">播放中</span> : null}
                </button>
              )
            })}
          </div>
        </section>
      ) : null}

      {overview ? (
        <section className="mt-4">
          <h2 className="mb-1.5 text-[13px] font-semibold text-white/90">简介</h2>
          <p className="text-xs leading-relaxed text-white/60">{overview}</p>
        </section>
      ) : null}

      <p className="mt-5 text-center text-[10px] text-white/30">将手机横放可获得全屏沉浸式播放体验</p>
    </div>
  )
}
