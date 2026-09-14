import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  Captions,
  CaptionsOff,
  Check,
  FastForward,
  Gauge,
  ListVideo,
  Loader2,
  Lock,
  Maximize,
  MessageSquareText,
  Minimize,
  Pause,
  PictureInPicture,
  Play,
  Rewind,
  SkipBack,
  SkipForward,
  Timer,
  Volume2,
  VolumeX,
} from 'lucide-react'
import type { SubtitleTrack } from '../api/subtitles'
import type { PlaybackQuality } from '../types'
import {
  formatPlaybackRate,
  normalizePlaybackRate,
  PLAYBACK_RATE_OPTIONS,
  stepPlaybackRate,
} from '../utils/playbackRate'
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

const SEEK_STEP_SEC = 10
const SEEK_REPEAT_MS = 160
const SEEK_APPLY_MS = 220
const SEEK_HINT_MS = 700
const PLAYBACK_RATE_REPEAT_MS = 140
const PLAYBACK_RATE_HINT_MS = 900

type SeekHint = {
  dir: 'back' | 'forward'
  seconds: number
}

function isPlayerSeekRange(target: EventTarget | null): boolean {
  return target instanceof HTMLInputElement && target.dataset.playerSeekRange === 'true'
}

function shouldIgnorePlayerSeekShortcut(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const control = target.closest<HTMLElement>(
    'input, textarea, select, [contenteditable="true"]',
  )
  if (control instanceof HTMLInputElement) {
    // 进度条需要继续走播放器的 10 秒快捷键；其它输入控件保留原生键盘行为。
    return !isPlayerSeekRange(control)
  }
  return Boolean(control || target.isContentEditable)
}

type PlayerControlsProps = {
  videoRef: React.RefObject<HTMLVideoElement>
  /** 用户级播放器音量（0 ~ 1），由播放页从数据库读取。 */
  volume?: number
  onVolumeChange?: (volume: number) => void
  onVolumeCommit?: (volume: number) => void
  /** 用户级播放倍速，由播放页从数据库读取。 */
  playbackRate?: number
  onPlaybackRateChange?: (rate: number) => void
  onPlaybackRateCommit?: (rate: number) => void
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
  qualities?: PlaybackQuality[]
  selectedQuality?: string
  onSelectQuality?: (quality: PlaybackQuality) => void
  showQuality?: boolean
  /** Media metadata duration (seconds). Used when HLS only knows transcoded length. */
  knownDuration?: number
  /** Absolute source offset of the current HLS session (seconds). */
  streamOffset?: number
  /** Absolute seek on the full timeline; return true when handled (e.g. HLS restart). */
  onSeekAbsolute?: (seconds: number) => boolean
}

export function PlayerControls({
  videoRef,
  volume: volumeProp = 1,
  onVolumeChange,
  onVolumeCommit,
  playbackRate: playbackRateProp = 1,
  onPlaybackRateChange,
  onPlaybackRateCommit,
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
  qualities = [],
  selectedQuality = '',
  onSelectQuality,
  showQuality = false,
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
  const [volume, setVolume] = useState(volumeProp)
  const [muted, setMuted] = useState(volumeProp === 0)
  const [fullscreen, setFullscreen] = useState(false)
  const [pip, setPip] = useState(false)
  const [controlsHovered, setControlsHovered] = useState(false)
  const [isScrubbing, setIsScrubbing] = useState(false)
  const [scrubValue, setScrubValue] = useState<number | null>(null)
  const [subtitleMenuOpen, setSubtitleMenuOpen] = useState(false)
  const subtitleMenuRef = useRef<HTMLDivElement | null>(null)
  const [qualityMenuOpen, setQualityMenuOpen] = useState(false)
  const qualityMenuRef = useRef<HTMLDivElement | null>(null)
  const [speedMenuOpen, setSpeedMenuOpen] = useState(false)
  const speedMenuRef = useRef<HTMLDivElement | null>(null)
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const controlsHoveredRef = useRef(false)
  const isScrubbingRef = useRef(false)
  const subtitleMenuOpenRef = useRef(false)
  const qualityMenuOpenRef = useRef(false)
  const speedMenuOpenRef = useRef(false)
  const danmakuOpenRef = useRef(false)
  const playlistOpenRef = useRef(false)
  const pendingSeekRef = useRef<number | null>(null)
  const applySeekRef = useRef<(absolute: number) => void>(() => undefined)
  const durationRef = useRef(0)
  const seekBurstRef = useRef<{ base: number; delta: number } | null>(null)
  const seekApplyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const seekHintTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastSeekAtRef = useRef(0)
  const playbackRateRef = useRef(normalizePlaybackRate(playbackRateProp))
  const playbackRateHintTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastPlaybackRateAtRef = useRef(0)
  const [seekHint, setSeekHint] = useState<SeekHint | null>(null)
  const [playbackRateHint, setPlaybackRateHint] = useState<number | null>(null)
  const [stageEl, setStageEl] = useState<HTMLElement | null>(null)

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
    qualityMenuOpenRef.current = qualityMenuOpen
  }, [qualityMenuOpen])

  useEffect(() => {
    speedMenuOpenRef.current = speedMenuOpen
  }, [speedMenuOpen])

  useEffect(() => {
    danmakuOpenRef.current = danmakuOpen
  }, [danmakuOpen])

  useEffect(() => {
    playlistOpenRef.current = playlistOpen
  }, [playlistOpen])

  useEffect(() => {
    durationRef.current = duration
  }, [duration])

  useEffect(() => {
    playbackRateRef.current = normalizePlaybackRate(playbackRateProp)
  }, [playbackRateProp])

  useEffect(() => {
    setStageEl(container())
  }, [container])

  // 音量由播放页按用户持久化；配置加载或切换对象后同步到当前 video。
  useEffect(() => {
    const el = video()
    if (!el) return
    const next = Math.min(1, Math.max(0, volumeProp))
    el.volume = next
    el.muted = next === 0
    setVolume(next)
    setMuted(next === 0)
  }, [video, volumeProp])

  // 倍速由播放页按用户持久化；配置加载、切换剧集或切换播放源后同步到当前 video。
  useEffect(() => {
    const el = video()
    if (!el) return
    const next = normalizePlaybackRate(playbackRateProp)
    if (Math.abs(el.playbackRate - next) > 0.001) {
      el.playbackRate = next
    }
  }, [video, playbackRateProp])

  // 点击控制栏外部时关闭字幕/画质/倍速菜单
  useEffect(() => {
    if (!subtitleMenuOpen && !qualityMenuOpen && !speedMenuOpen) return
    const onDocClick = (e: MouseEvent) => {
      if (subtitleMenuRef.current && !subtitleMenuRef.current.contains(e.target as Node)) {
        setSubtitleMenuOpen(false)
      }
      if (qualityMenuRef.current && !qualityMenuRef.current.contains(e.target as Node)) {
        setQualityMenuOpen(false)
      }
      if (speedMenuRef.current && !speedMenuRef.current.contains(e.target as Node)) {
        setSpeedMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [subtitleMenuOpen, qualityMenuOpen, speedMenuOpen])

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
        !qualityMenuOpenRef.current &&
        !speedMenuOpenRef.current &&
        !danmakuOpenRef.current &&
        !playlistOpenRef.current
      ) {
        hideTimerRef.current = setTimeout(() => {
          if (
            !controlsHoveredRef.current &&
            !isScrubbingRef.current &&
            !subtitleMenuOpenRef.current &&
            !qualityMenuOpenRef.current &&
            !speedMenuOpenRef.current &&
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
    if (controlsHovered || isScrubbing || subtitleMenuOpen || qualityMenuOpen || speedMenuOpen || danmakuOpen || playlistOpen) {
      onUiVisibleChange(true)
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    } else {
      const el = video()
      if (el && !el.paused) {
        if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
        hideTimerRef.current = setTimeout(() => onUiVisibleChange(false), 3000)
      }
    }
  }, [controlsHovered, isScrubbing, subtitleMenuOpen, qualityMenuOpen, speedMenuOpen, danmakuOpen, playlistOpen, onUiVisibleChange, video])

  const togglePlay = () => {
    const el = video()
    if (!el) return
    if (el.paused) void el.play()?.catch(() => undefined)
    else el.pause()
  }

  const changePlaybackRate = useCallback((next: number) => {
    const normalized = normalizePlaybackRate(next)
    playbackRateRef.current = normalized
    onPlaybackRateChange?.(normalized)
    setPlaybackRateHint(normalized)
    if (playbackRateHintTimerRef.current) {
      clearTimeout(playbackRateHintTimerRef.current)
    }
    playbackRateHintTimerRef.current = setTimeout(() => {
      setPlaybackRateHint(null)
      playbackRateHintTimerRef.current = null
    }, PLAYBACK_RATE_HINT_MS)
  }, [onPlaybackRateChange])

  const commitPlaybackRate = useCallback(() => {
    onPlaybackRateCommit?.(playbackRateRef.current)
  }, [onPlaybackRateCommit])

  const selectPlaybackRate = useCallback((next: number) => {
    const normalized = normalizePlaybackRate(next)
    changePlaybackRate(normalized)
    onPlaybackRateCommit?.(normalized)
  }, [changePlaybackRate, onPlaybackRateCommit])

  const applyAbsoluteSeek = useCallback(
    (absolute: number) => {
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
    },
    [onSeekAbsolute, streamOffset, video],
  )

  useEffect(() => {
    applySeekRef.current = applyAbsoluteSeek
  }, [applyAbsoluteSeek])

  const handleSeekChange = (v: number) => {
    setScrubValue(v)
    setCurrentTime(v)
    if (!isScrubbing) {
      applyAbsoluteSeek(v)
    }
  }

  const handleSeekStart = () => {
    if (seekApplyTimerRef.current) {
      clearTimeout(seekApplyTimerRef.current)
      seekApplyTimerRef.current = null
    }
    seekBurstRef.current = null
    setIsScrubbing(true)
    onUiVisibleChange(true)
    if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
  }

  const handleSeekEnd = (v: number) => {
    applyAbsoluteSeek(v)
    setIsScrubbing(false)
    setScrubValue(null)
  }

  const revealControls = useCallback(() => {
    onUiVisibleChange(true)
    if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    const el = video()
    if (
      !el ||
      el.paused ||
      controlsHoveredRef.current ||
      isScrubbingRef.current ||
      subtitleMenuOpenRef.current ||
      qualityMenuOpenRef.current ||
      danmakuOpenRef.current ||
      playlistOpenRef.current
    ) {
      return
    }
    hideTimerRef.current = setTimeout(() => {
      if (
        !controlsHoveredRef.current &&
        !isScrubbingRef.current &&
        !subtitleMenuOpenRef.current &&
        !qualityMenuOpenRef.current &&
        !danmakuOpenRef.current &&
        !playlistOpenRef.current
      ) {
        onUiVisibleChange(false)
      }
    }, 3000)
  }, [onUiVisibleChange, video])

  const queueRelativeSeek = useCallback((delta: number, immediate: boolean) => {
    const el = video()
    if (!el) return
    if (!seekBurstRef.current) {
      const base = pendingSeekRef.current ?? streamOffset + (el.currentTime || 0)
      seekBurstRef.current = { base, delta: 0 }
    }
    seekBurstRef.current.delta += delta
    const max = durationRef.current > 0 ? durationRef.current : Number.POSITIVE_INFINITY
    const target = Math.min(max, Math.max(0, seekBurstRef.current.base + seekBurstRef.current.delta))
    const applied = target - seekBurstRef.current.base
    seekBurstRef.current.delta = applied
    if (applied === 0) {
      if (!seekApplyTimerRef.current) {
        seekBurstRef.current = null
        setSeekHint({
          dir: delta < 0 ? 'back' : 'forward',
          seconds: 0,
        })
        if (seekHintTimerRef.current) clearTimeout(seekHintTimerRef.current)
        seekHintTimerRef.current = setTimeout(() => setSeekHint(null), SEEK_HINT_MS)
      }
      return
    }

    pendingSeekRef.current = target
    setCurrentTime(target)
    setSeekHint({
      dir: applied < 0 ? 'back' : 'forward',
      seconds: Math.abs(Math.round(applied)),
    })
    revealControls()

    if (seekHintTimerRef.current) clearTimeout(seekHintTimerRef.current)
    seekHintTimerRef.current = setTimeout(() => setSeekHint(null), SEEK_HINT_MS)
    if (immediate) {
      if (seekApplyTimerRef.current) {
        clearTimeout(seekApplyTimerRef.current)
        seekApplyTimerRef.current = null
      }
      const burst = seekBurstRef.current
      seekBurstRef.current = null
      applySeekRef.current(burst.base + burst.delta)
      return
    }
    if (seekApplyTimerRef.current) clearTimeout(seekApplyTimerRef.current)
    seekApplyTimerRef.current = setTimeout(() => {
      const burst = seekBurstRef.current
      seekBurstRef.current = null
      seekApplyTimerRef.current = null
      if (!burst) return
      applySeekRef.current(burst.base + burst.delta)
    }, SEEK_APPLY_MS)
  }, [revealControls, streamOffset, video])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
      if (shouldIgnorePlayerSeekShortcut(e.target)) return
      if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        e.preventDefault()
        const now = Date.now()
        if (e.repeat && now - lastPlaybackRateAtRef.current < PLAYBACK_RATE_REPEAT_MS) return
        lastPlaybackRateAtRef.current = now
        changePlaybackRate(
          stepPlaybackRate(playbackRateRef.current, e.key === 'ArrowUp' ? 1 : -1),
        )
        return
      }
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
      e.preventDefault()
      const now = Date.now()
      if (e.repeat && now - lastSeekAtRef.current < SEEK_REPEAT_MS) return
      const immediate = !e.repeat && now - lastSeekAtRef.current >= SEEK_APPLY_MS
      lastSeekAtRef.current = now
      e.preventDefault()
      queueRelativeSeek(e.key === 'ArrowLeft' ? -SEEK_STEP_SEC : SEEK_STEP_SEC, immediate)
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [changePlaybackRate, queueRelativeSeek])

  useEffect(() => {
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
      if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return
      if (shouldIgnorePlayerSeekShortcut(e.target)) return
      e.preventDefault()
      commitPlaybackRate()
    }
    window.addEventListener('keyup', onKeyUp, true)
    return () => window.removeEventListener('keyup', onKeyUp, true)
  }, [commitPlaybackRate])

  useEffect(() => {
    return () => {
      if (seekApplyTimerRef.current) clearTimeout(seekApplyTimerRef.current)
      if (seekHintTimerRef.current) clearTimeout(seekHintTimerRef.current)
      if (playbackRateHintTimerRef.current) clearTimeout(playbackRateHintTimerRef.current)
    }
  }, [])

  const changeVolume = (v: number) => {
    const el = video()
    if (!el) return
    const next = Math.min(1, Math.max(0, v))
    el.volume = next
    el.muted = next === 0
    setVolume(next)
    setMuted(next === 0)
    onVolumeChange?.(next)
  }

  const commitVolume = () => {
    const el = video()
    onVolumeCommit?.(el?.volume ?? volume)
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
  const selectedQualityLabel = qualities.find((quality) => quality.id === selectedQuality)?.label ?? ''
  const qualityGroups = [
    { key: 'cloud', label: '115 云端', items: qualities.filter((quality) => quality.source !== 'local') },
    { key: 'local', label: '本地 HLS', items: qualities.filter((quality) => quality.source === 'local') },
  ].filter((group) => group.items.length > 0)

  const seekOverlay = seekHint && stageEl
    ? createPortal(
        <div className="pointer-events-none absolute inset-0 z-30">
          <div
            className={`absolute top-1/2 flex w-36 -translate-y-1/2 flex-col items-center justify-center rounded-full bg-black/55 px-3 py-5 text-white shadow-lg backdrop-blur-sm ${
              seekHint.dir === 'back' ? 'left-[8%] sm:left-[12%]' : 'right-[8%] sm:right-[12%]'
            }`}
          >
            {seekHint.dir === 'back' ? <Rewind size={28} /> : <FastForward size={28} />}
            <span className="mt-1 text-center text-sm font-medium leading-tight">
              {seekHint.seconds > 0
                ? seekHint.dir === 'back'
                  ? '回退'
                  : '快进'
                : seekHint.dir === 'back'
                  ? '已到开头'
                  : '已到结尾'}
              {seekHint.seconds > 0 && (
                <>
                  <br />
                  <span className="tabular-nums">{seekHint.seconds} 秒</span>
                </>
              )}
            </span>
          </div>
        </div>,
        stageEl,
      )
    : null

  const playbackRateOverlay = playbackRateHint && stageEl
    ? createPortal(
        <div className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center">
          <div className="flex items-center gap-2 rounded-full bg-black/65 px-5 py-3 text-white shadow-lg backdrop-blur-sm">
            <Timer size={22} />
            <span className="text-sm font-semibold tabular-nums">
              {formatPlaybackRate(playbackRateHint)} 倍速
            </span>
          </div>
        </div>,
        stageEl,
      )
    : null

  return (
    <>
    {seekOverlay}
    {playbackRateOverlay}
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
            data-player-seek-range="true"
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

        {showQuality && qualities.length > 0 && onSelectQuality && (
          <div className="relative" ref={qualityMenuRef}>
            <button
              onClick={() => {
                setSubtitleMenuOpen(false)
                setSpeedMenuOpen(false)
                setQualityMenuOpen((v) => !v)
              }}
              className="flex items-center gap-1 rounded-full p-1.5 transition hover:bg-white/15"
              title="画质"
            >
              <Gauge size={18} className={qualityMenuOpen ? 'text-rose-400' : 'text-white/80'} />
              <span className="hidden text-[10px] font-medium text-white/80 sm:inline">
                {selectedQualityLabel || '画质'}
              </span>
            </button>
            {qualityMenuOpen && (
              <div className="absolute bottom-11 right-0 z-30 min-w-48 overflow-hidden rounded-xl border border-white/15 bg-black/85 p-1 shadow-2xl backdrop-blur">
                {qualityGroups.map((group) => (
                  <div key={group.key}>
                    <p className="px-3 pb-1 pt-2 text-[10px] uppercase tracking-wide text-white/40">
                      {group.label}
                    </p>
                    {group.items.map((quality) => {
                      const current = quality.id === selectedQuality
                      return (
                        <button
                          key={`${quality.source}-${quality.id}`}
                          type="button"
                          onClick={() => {
                            onSelectQuality(quality)
                            setQualityMenuOpen(false)
                          }}
                          className={`flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left text-xs transition ${
                            current
                              ? 'text-rose-400 hover:bg-white/10'
                              : quality.requires_vip
                                ? 'text-white/55 hover:bg-white/10'
                                : 'text-white/85 hover:bg-white/10'
                          }`}
                          title={quality.note || quality.label}
                        >
                          <span className="truncate">{quality.label}</span>
                          {quality.requires_transcode && (
                            <span className="ml-auto flex items-center gap-1 rounded bg-white/10 px-1.5 py-0.5 text-[9px] text-amber-300">
                              <Loader2 size={10} />
                              转码
                            </span>
                          )}
                          {quality.requires_vip && <Lock size={11} className="ml-auto text-amber-300" />}
                          {current && !quality.requires_transcode && (
                            <Check size={13} className="ml-auto text-rose-400" />
                          )}
                        </button>
                      )
                    })}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        <div className="relative" ref={speedMenuRef}>
          <button
            onClick={() => {
              setSubtitleMenuOpen(false)
              setQualityMenuOpen(false)
              setSpeedMenuOpen((v) => !v)
            }}
            className="flex items-center gap-1 rounded-full p-1.5 transition hover:bg-white/15"
            title="播放速度（↑/↓ 调节）"
          >
            <Timer size={18} className={speedMenuOpen ? 'text-rose-400' : 'text-white/80'} />
            <span className="text-[10px] font-semibold tabular-nums text-white/80">
              {formatPlaybackRate(playbackRateProp)}
            </span>
          </button>
          {speedMenuOpen && (
            <div className="absolute bottom-11 right-0 z-30 min-w-36 overflow-hidden rounded-xl border border-white/15 bg-black/85 p-1 shadow-2xl backdrop-blur">
              <p className="px-3 pb-1 pt-2 text-[10px] uppercase tracking-wide text-white/40">
                播放速度
              </p>
              {PLAYBACK_RATE_OPTIONS.map((rate) => {
                const current = normalizePlaybackRate(playbackRateProp) === rate
                return (
                  <button
                    key={rate}
                    type="button"
                    onClick={() => {
                      selectPlaybackRate(rate)
                      setSpeedMenuOpen(false)
                    }}
                    className={`flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left text-xs transition ${
                      current ? 'text-rose-400 hover:bg-white/10' : 'text-white/85 hover:bg-white/10'
                    }`}
                  >
                    <span className="tabular-nums">{formatPlaybackRate(rate)}</span>
                    {current && <Check size={13} className="ml-auto text-rose-400" />}
                  </button>
                )
              })}
            </div>
          )}
        </div>

        {subs.length > 0 && (
          <div className="relative" ref={subtitleMenuRef}>
            <button
              onClick={() => {
                setQualityMenuOpen(false)
                setSpeedMenuOpen(false)
                setSubtitleMenuOpen((v) => !v)
              }}
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
          onPointerUp={commitVolume}
          onKeyUp={commitVolume}
          onTouchEnd={commitVolume}
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
    </>
  )
}
