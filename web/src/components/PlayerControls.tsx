import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Captions,
  CaptionsOff,
  ListVideo,
  Maximize,
  MessageSquareText,
  Minimize,
  Pause,
  PictureInPicture,
  Play,
  SkipBack,
  SkipForward,
  Volume2,
  VolumeX,
} from 'lucide-react'
import type { SubtitleTrack } from '../api/subtitles'
import type { SubtitleChineseMode } from '../utils/subtitleChinese'
import {
  SUBTITLE_POSITION_OPTIONS,
  SUBTITLE_STYLE_OPTIONS,
  type SubtitlePosition,
  type SubtitleStylePreset,
} from '../utils/subtitleDisplay'

// PlayerControls — custom bottom control bar replacing the native <video
// controls> (which cannot host custom buttons). The danmaku toggle sits right
// next to the volume control. The bar auto-hides while playing and reappears
// on mouse movement; it stays visible while paused or when hovering/interacting.

function formatTime(s: number): string {
  if (!Number.isFinite(s) || s < 0) s = 0
  const m = Math.floor(s / 60)
  const sec = Math.floor(s % 60)
  return `${m}:${String(sec).padStart(2, '0')}`
}

type PlayerControlsProps = {
  videoRef: React.RefObject<HTMLVideoElement>
  uiVisible: boolean
  onUiVisibleChange: (visible: boolean) => void
  subs: SubtitleTrack[]
  /** 当前激活字幕轨道：-1=关闭，0..n-1=对应轨道。 */
  subtitleIndex: number
  onSelectSubtitle: (index: number) => void
  subtitleChineseMode: SubtitleChineseMode
  onSubtitleChineseModeChange: (mode: SubtitleChineseMode) => void
  subtitlePosition: SubtitlePosition
  onSubtitlePositionChange: (position: SubtitlePosition) => void
  subtitleStyle: SubtitleStylePreset
  onSubtitleStyleChange: (style: SubtitleStylePreset) => void
  danmakuOpen: boolean
  danmakuEnabled: boolean
  onToggleDanmaku: () => void
  hasPrevEpisode?: boolean
  hasNextEpisode?: boolean
  onPrevEpisode?: () => void
  onNextEpisode?: () => void
  prevEpisodeTitle?: string
  nextEpisodeTitle?: string
  playlistOpen?: boolean
  hasPlaylist?: boolean
  onTogglePlaylist?: () => void
  /** Media metadata duration (seconds). Used when HLS only knows transcoded length. */
  knownDuration?: number
  /** Absolute source offset of the current HLS session (seconds). */
  streamOffset?: number
  /** Absolute seek on the full timeline; return true when handled (e.g. HLS restart). */
  onSeekAbsolute?: (seconds: number) => boolean
}

export function PlayerControls({
  videoRef,
  uiVisible,
  onUiVisibleChange,
  subs,
  subtitleIndex,
  onSelectSubtitle,
  subtitleChineseMode,
  onSubtitleChineseModeChange,
  subtitlePosition,
  onSubtitlePositionChange,
  subtitleStyle,
  onSubtitleStyleChange,
  danmakuOpen,
  danmakuEnabled,
  onToggleDanmaku,
  hasPrevEpisode = false,
  hasNextEpisode = false,
  onPrevEpisode,
  onNextEpisode,
  prevEpisodeTitle,
  nextEpisodeTitle,
  playlistOpen = false,
  hasPlaylist = false,
  onTogglePlaylist,
  knownDuration = 0,
  streamOffset = 0,
  onSeekAbsolute,
}: PlayerControlsProps) {
  const video = useCallback(() => videoRef.current, [videoRef])
  const container = useCallback(() =>
    videoRef.current?.closest<HTMLElement>('[data-player-stage]') ??
    videoRef.current?.parentElement ??
    null, [videoRef])

  const [playing, setPlaying] = useState(false)
  const [currentTime, setCurrentTime] = useState(0)
  const [duration, setDuration] = useState(0)
  const [volume, setVolume] = useState(1)
  const [muted, setMuted] = useState(false)
  const [fullscreen, setFullscreen] = useState(false)
  const [pip, setPip] = useState(false)
  const [controlsHovered, setControlsHovered] = useState(false)
  const [isScrubbing, setIsScrubbing] = useState(false)
  const [scrubValue, setScrubValue] = useState<number | null>(null)
  const [subtitleMenuOpen, setSubtitleMenuOpen] = useState(false)
  const subtitleMenuRef = useRef<HTMLDivElement | null>(null)
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const controlsHoveredRef = useRef(false)
  const isScrubbingRef = useRef(false)
  const subtitleMenuOpenRef = useRef(false)
  const danmakuOpenRef = useRef(false)
  const playlistOpenRef = useRef(false)
  const pendingSeekRef = useRef<number | null>(null)

  useEffect(() => {
    controlsHoveredRef.current = controlsHovered
  }, [controlsHovered])

  useEffect(() => {
    isScrubbingRef.current = isScrubbing
  }, [isScrubbing])

  useEffect(() => {
    subtitleMenuOpenRef.current = subtitleMenuOpen
  }, [subtitleMenuOpen])

  useEffect(() => {
    danmakuOpenRef.current = danmakuOpen
  }, [danmakuOpen])

  useEffect(() => {
    playlistOpenRef.current = playlistOpen
  }, [playlistOpen])

  // 点击控制栏外部时关闭字幕菜单
  useEffect(() => {
    if (!subtitleMenuOpen) return
    const onDocClick = (e: MouseEvent) => {
      if (subtitleMenuRef.current && !subtitleMenuRef.current.contains(e.target as Node)) {
        setSubtitleMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [subtitleMenuOpen])

  // 播放时 3 秒无操作自动隐藏控制栏；暂停/悬停/拖动进度条/打开菜单时保持显示。
  // 监听挂在整个播放器舞台容器（data-player-stage）上，避免光标移到控制栏时因离开视频画面而误触发 mouseleave。
  useEffect(() => {
    const el = video()
    const stage = container()
    if (!el || !stage) return

    const resetTimer = () => {
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
      if (
        !el.paused &&
        !controlsHoveredRef.current &&
        !isScrubbingRef.current &&
        !subtitleMenuOpenRef.current &&
        !danmakuOpenRef.current &&
        !playlistOpenRef.current
      ) {
        hideTimerRef.current = setTimeout(() => {
          if (
            !controlsHoveredRef.current &&
            !isScrubbingRef.current &&
            !subtitleMenuOpenRef.current &&
            !danmakuOpenRef.current &&
            !playlistOpenRef.current
          ) {
            onUiVisibleChange(false)
          }
        }, 3000)
      }
    }

    const onMove = () => {
      onUiVisibleChange(true)
      resetTimer()
    }

    const onLeave = (e: MouseEvent) => {
      // 仅当光标真正移出 stage 容器时才处理
      if (e.relatedTarget && stage.contains(e.relatedTarget as Node)) {
        return
      }
      if (el.paused || controlsHoveredRef.current || isScrubbingRef.current || playlistOpenRef.current) return
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
      onUiVisibleChange(false)
    }

    const syncPlay = () => {
      setPlaying(!el.paused)
      onMove()
    }
    const syncTime = () => {
      if (isScrubbingRef.current) return
      if (pendingSeekRef.current !== null) {
        const curAbs = streamOffset + el.currentTime
        if (Math.abs(curAbs - pendingSeekRef.current) < 3 && el.currentTime > 0.1) {
          pendingSeekRef.current = null
          setCurrentTime(curAbs)
        } else {
          setCurrentTime(pendingSeekRef.current)
        }
        return
      }
      setCurrentTime(streamOffset + el.currentTime)
    }
    const syncMeta = () => {
      const streamDur = Number.isFinite(el.duration) ? el.duration : 0
      setDuration(Math.max(knownDuration || 0, streamOffset + streamDur))
      if (isScrubbingRef.current) return
      if (pendingSeekRef.current !== null) {
        setCurrentTime(pendingSeekRef.current)
        return
      }
      setCurrentTime(streamOffset + el.currentTime)
    }
    const syncVolume = () => {
      setVolume(el.volume)
      setMuted(el.muted)
    }
    const syncFullscreen = () => setFullscreen(Boolean(document.fullscreenElement))
    const syncPip = () => setPip(document.pictureInPictureElement === el)

    stage.addEventListener('mousemove', onMove)
    stage.addEventListener('mouseleave', onLeave)

    el.addEventListener('play', syncPlay)
    el.addEventListener('playing', syncPlay)
    el.addEventListener('pause', syncPlay)
    el.addEventListener('timeupdate', syncTime)
    el.addEventListener('durationchange', syncMeta)
    el.addEventListener('loadedmetadata', syncMeta)
    el.addEventListener('volumechange', syncVolume)
    document.addEventListener('fullscreenchange', syncFullscreen)
    el.addEventListener('enterpictureinpicture', syncPip)
    el.addEventListener('leavepictureinpicture', syncPip)

    syncMeta()
    syncVolume()
    syncFullscreen()
    return () => {
      stage.removeEventListener('mousemove', onMove)
      stage.removeEventListener('mouseleave', onLeave)
      el.removeEventListener('play', syncPlay)
      el.removeEventListener('playing', syncPlay)
      el.removeEventListener('pause', syncPlay)
      el.removeEventListener('timeupdate', syncTime)
      el.removeEventListener('durationchange', syncMeta)
      el.removeEventListener('loadedmetadata', syncMeta)
      el.removeEventListener('volumechange', syncVolume)
      document.removeEventListener('fullscreenchange', syncFullscreen)
      el.removeEventListener('enterpictureinpicture', syncPip)
      el.removeEventListener('leavepictureinpicture', syncPip)
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    }
  }, [container, video, knownDuration, streamOffset, onUiVisibleChange])

  // Keep the scrubber max in sync when metadata duration arrives after mount.
  useEffect(() => {
    const el = video()
    const streamDur = el && Number.isFinite(el.duration) ? el.duration : 0
    setDuration(Math.max(knownDuration || 0, streamOffset + streamDur))
  }, [knownDuration, streamOffset, video])

  // 当悬停或菜单状态改变时，更新控制栏计时器
  useEffect(() => {
    if (controlsHovered || isScrubbing || subtitleMenuOpen || danmakuOpen || playlistOpen) {
      onUiVisibleChange(true)
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    } else {
      const el = video()
      if (el && !el.paused) {
        if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
        hideTimerRef.current = setTimeout(() => onUiVisibleChange(false), 3000)
      }
    }
  }, [controlsHovered, isScrubbing, subtitleMenuOpen, danmakuOpen, playlistOpen, onUiVisibleChange, video])

  const togglePlay = () => {
    const el = video()
    if (!el) return
    if (el.paused) void el.play()?.catch(() => undefined)
    else el.pause()
  }

  const applyAbsoluteSeek = (absolute: number) => {
    const el = video()
    if (!el) return
    if (onSeekAbsolute?.(absolute)) {
      pendingSeekRef.current = absolute
      setCurrentTime(absolute)
      return
    }
    const local = Math.max(0, absolute - streamOffset)
    el.currentTime = local
    setCurrentTime(streamOffset + local)
  }

  const handleSeekChange = (v: number) => {
    setScrubValue(v)
    setCurrentTime(v)
    if (!isScrubbing) {
      applyAbsoluteSeek(v)
    }
  }

  const handleSeekStart = () => {
    setIsScrubbing(true)
    onUiVisibleChange(true)
    if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
  }

  const handleSeekEnd = (v: number) => {
    applyAbsoluteSeek(v)
    setIsScrubbing(false)
    setScrubValue(null)
  }

  const changeVolume = (v: number) => {
    const el = video()
    if (!el) return
    el.volume = v
    el.muted = v === 0
    setVolume(v)
    setMuted(v === 0)
  }

  const toggleMute = () => {
    const el = video()
    if (!el) return
    el.muted = !el.muted
  }

  const toggleFullscreen = () => {
    const el = container()
    if (!el) return
    if (document.fullscreenElement) {
      void document.exitFullscreen()
    } else {
      void el.requestFullscreen?.()
    }
  }

  const togglePip = () => {
    const el = video()
    if (!el) return
    if (document.pictureInPictureElement) {
      void document.exitPictureInPicture()
    } else {
      void el.requestPictureInPicture?.()
    }
  }

  const pipSupported =
    typeof document !== 'undefined' &&
    'pictureInPictureEnabled' in document &&
    document.pictureInPictureEnabled

  const displayTime = isScrubbing && scrubValue !== null ? scrubValue : currentTime
  const selectedSubtitle = subtitleIndex >= 0 ? subs[subtitleIndex] : undefined
  const canConvertSelectedSubtitle =
    selectedSubtitle?.source === 'external' &&
    (selectedSubtitle.delivery === 'webvtt' || selectedSubtitle.delivery === 'ass')
  const canAdjustSelectedSubtitle = selectedSubtitle?.delivery === 'webvtt'
  const usesOriginalASS = selectedSubtitle?.delivery === 'ass'

  return (
    <div
      className={`pointer-events-auto absolute inset-x-0 bottom-0 z-20 bg-gradient-to-t from-black/80 via-black/40 to-transparent px-3 pb-3 pt-14 transition-opacity duration-300 ${
        uiVisible ? 'opacity-100' : 'pointer-events-none opacity-0'
      }`}
      onMouseEnter={() => setControlsHovered(true)}
      onMouseLeave={() => setControlsHovered(false)}
      onClick={(e) => e.stopPropagation()}
      onPointerUp={(e) => e.stopPropagation()}
    >
      <div className="flex flex-wrap items-center gap-2 text-white sm:flex-nowrap sm:gap-2.5">
        {/* 上一集 */}
        <button
          onClick={onPrevEpisode}
          disabled={!hasPrevEpisode}
          className={`rounded-full p-1.5 transition ${
            hasPrevEpisode
              ? 'hover:bg-white/15 text-white cursor-pointer'
              : 'text-white/30 cursor-not-allowed opacity-40'
          }`}
          title={hasPrevEpisode ? (prevEpisodeTitle ? `上一集：${prevEpisodeTitle} ([)` : '上一集 ([)') : '没有上一集'}
        >
          <SkipBack size={18} />
        </button>

        {/* 播放 / 暂停 */}
        <button
          onClick={togglePlay}
          className="rounded-full p-1.5 transition hover:bg-white/15"
          title={playing ? '暂停 (Space)' : '播放 (Space)'}
        >
          {playing ? <Pause size={20} /> : <Play size={20} />}
        </button>

        {/* 下一集 */}
        <button
          onClick={onNextEpisode}
          disabled={!hasNextEpisode}
          className={`rounded-full p-1.5 transition ${
            hasNextEpisode
              ? 'hover:bg-white/15 text-white cursor-pointer'
              : 'text-white/30 cursor-not-allowed opacity-40'
          }`}
          title={hasNextEpisode ? (nextEpisodeTitle ? `下一集：${nextEpisodeTitle} (])` : '下一集 (])') : '没有下一集'}
        >
          <SkipForward size={18} />
        </button>

        <div className="order-2 flex min-w-0 basis-full items-center gap-2 sm:order-none sm:basis-auto sm:flex-1">
          <input
            type="range"
            min={0}
            max={duration || 0}
            step={0.1}
            value={displayTime}
            onMouseDown={handleSeekStart}
            onTouchStart={handleSeekStart}
            onChange={(e) => handleSeekChange(Number(e.target.value))}
            onMouseUp={(e) => handleSeekEnd(Number((e.target as HTMLInputElement).value))}
            onTouchEnd={(e) => handleSeekEnd(Number((e.target as HTMLInputElement).value))}
            className="min-w-0 flex-1 cursor-pointer accent-rose-500"
            aria-label="播放进度"
          />
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-white/85 sm:text-xs">
            {formatTime(displayTime)} / {formatTime(duration)}
          </span>
        </div>

        {pipSupported && (
          <button
            onClick={togglePip}
            className="rounded-full p-1.5 transition hover:bg-white/15"
            title={pip ? '退出画中画' : '画中画'}
          >
            <PictureInPicture size={18} className={pip ? 'text-rose-400' : ''} />
          </button>
        )}

        {subs.length > 0 && (
          <div className="relative" ref={subtitleMenuRef}>
            <button
              onClick={() => setSubtitleMenuOpen((v) => !v)}
              className="rounded-full p-1.5 transition hover:bg-white/15"
              title="字幕"
            >
              {subtitleIndex >= 0 ? (
                <Captions size={18} className="text-rose-400" />
              ) : (
                <CaptionsOff size={18} className="text-white/70" />
              )}
            </button>
            {subtitleMenuOpen && (
              <div className="absolute bottom-11 right-0 z-30 max-h-[70vh] min-w-44 overflow-y-auto rounded-xl border border-white/15 bg-black/85 p-1 shadow-2xl backdrop-blur">
                <button
                  type="button"
                  onClick={() => {
                    onSelectSubtitle(-1)
                    setSubtitleMenuOpen(false)
                  }}
                  className={`flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left text-xs transition hover:bg-white/10 ${
                    subtitleIndex < 0 ? 'text-rose-400' : 'text-white/85'
                  }`}
                >
                  关闭字幕
                </button>
                {subs.map((track, index) => (
                  <button
                    key={track.path}
                    type="button"
                    onClick={() => {
                      onSelectSubtitle(index)
                      setSubtitleMenuOpen(false)
                    }}
                    className={`flex w-full items-center gap-2 truncate rounded-lg px-3 py-1.5 text-left text-xs transition hover:bg-white/10 ${
                      subtitleIndex === index ? 'text-rose-400' : 'text-white/85'
                    }`}
                    title={track.label || track.lang}
                  >
                    <span className="truncate">
                      {track.label || track.lang || `字幕 ${index + 1}`}
                      {track.delivery === 'ass' ? ' · ASS' : ''}
                    </span>
                    {subtitleIndex === index && <span className="ml-auto text-rose-400">●</span>}
                  </button>
                ))}
                {canAdjustSelectedSubtitle && (
                  <div className="mt-1 border-t border-white/10 pt-1">
                    <p className="px-3 py-1 text-[10px] text-white/45">字幕位置</p>
                    <div className="flex flex-wrap gap-1 px-2 pb-1">
                      {SUBTITLE_POSITION_OPTIONS.map(([value, label]) => (
                        <button
                          key={value}
                          type="button"
                          onClick={() => onSubtitlePositionChange(value)}
                          className={`rounded-md px-2 py-1 text-[10px] transition ${
                            subtitlePosition === value
                              ? 'bg-rose-500/20 text-rose-300'
                              : 'bg-white/5 text-white/70 hover:bg-white/10'
                          }`}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                    <p className="px-3 py-1 text-[10px] text-white/45">字幕样式</p>
                    <div className="flex flex-wrap gap-1 px-2 pb-1">
                      {SUBTITLE_STYLE_OPTIONS.map(([value, label]) => (
                        <button
                          key={value}
                          type="button"
                          onClick={() => onSubtitleStyleChange(value)}
                          className={`rounded-md px-2 py-1 text-[10px] transition ${
                            subtitleStyle === value
                              ? 'bg-rose-500/20 text-rose-300'
                              : 'bg-white/5 text-white/70 hover:bg-white/10'
                          }`}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                  </div>
                )}
                {usesOriginalASS && (
                  <p className="mx-2 mt-1 border-t border-white/10 px-1 pt-2 text-[10px] leading-relaxed text-white/45">
                    ASS/SSA 使用字幕文件自带的样式与位置，不做统一覆盖。
                  </p>
                )}
                {canConvertSelectedSubtitle && (
                  <div className="mt-1 border-t border-white/10 pt-1">
                    <p className="px-3 py-1 text-[10px] text-white/45">外挂字幕简繁转换</p>
                    {(
                      [
                        ['original', '保持原文'],
                        ['simplified', '转换为简体'],
                        ['traditional', '转换为繁体'],
                      ] as const
                    ).map(([mode, label]) => (
                      <button
                        key={mode}
                        type="button"
                        onClick={() => {
                          onSubtitleChineseModeChange(mode)
                          setSubtitleMenuOpen(false)
                        }}
                        className={`flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left text-xs transition hover:bg-white/10 ${
                          subtitleChineseMode === mode ? 'text-rose-400' : 'text-white/85'
                        }`}
                      >
                        <span>{label}</span>
                        {subtitleChineseMode === mode && (
                          <span className="ml-auto text-rose-400">●</span>
                        )}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* 选集 / 播放列表按钮 */}
        {onTogglePlaylist && (
          <button
            onClick={onTogglePlaylist}
            disabled={!hasPlaylist}
            className={
              'flex items-center gap-1.5 rounded-full px-2.5 py-1.5 text-xs font-medium transition ' +
              (!hasPlaylist
                ? 'bg-white/5 text-white/30 cursor-not-allowed opacity-50'
                : playlistOpen
                ? 'bg-rose-500 text-white hover:bg-rose-600'
                : 'bg-white/10 text-white/80 hover:bg-white/20')
            }
            title={hasPlaylist ? '选集列表' : '当前无更多剧集'}
          >
            <ListVideo size={15} />
              <span className="hidden sm:inline">选集</span>
          </button>
        )}

        {/* 弹幕按钮 */}
        <button
          onClick={onToggleDanmaku}
          className={
            'flex items-center gap-1.5 rounded-full px-2.5 py-1.5 text-xs font-medium transition ' +
            (danmakuOpen || danmakuEnabled
              ? 'bg-rose-500/90 text-white hover:bg-rose-500'
              : 'bg-white/10 text-white/80 hover:bg-white/20')
          }
          title="弹幕设置"
        >
          <MessageSquareText size={15} />
          <span className="hidden sm:inline">弹幕</span>
          {danmakuEnabled && <span className="h-1.5 w-1.5 rounded-full bg-lime-400" />}
        </button>

        <button
          onClick={toggleMute}
          className="rounded-full p-1.5 transition hover:bg-white/15"
          title={muted || volume === 0 ? '取消静音 (M)' : '静音 (M)'}
        >
          {muted || volume === 0 ? <VolumeX size={18} /> : <Volume2 size={18} />}
        </button>
        <input
          type="range"
          min={0}
          max={1}
          step={0.05}
          value={muted ? 0 : volume}
          onChange={(e) => changeVolume(Number(e.target.value))}
          className="hidden w-16 accent-rose-500 sm:block"
          aria-label="音量"
        />

        <button
          onClick={toggleFullscreen}
          className="rounded-full p-1.5 transition hover:bg-white/15"
          title={fullscreen ? '退出全屏 (F)' : '全屏 (F)'}
        >
          {fullscreen ? <Minimize size={18} /> : <Maximize size={18} />}
        </button>
      </div>
    </div>
  )
}