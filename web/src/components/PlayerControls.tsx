import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  Captions,
  CaptionsOff,
  Check,
  FastForward,
  Layers,
  ListVideo,
  Loader2,
  Lock,
  Maximize,
  Minimize,
  Pause,
  PictureInPicture,
  Play,
  Rewind,
  Rotate3d,
  Settings2,
  SkipBack,
  SkipForward,
  Timer,
  Volume2,
  VolumeX,
} from 'lucide-react'
import type { SubtitleTrack } from '../api/subtitles'
import type { PlaybackQuality } from '../types'
import type { Vr360Profile } from '../utils/vr360'
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
import {
  PLAYER_ICON_BUTTON,
  PLAYER_MENU_DIVIDER,
  PLAYER_MENU_ITEM,
  PLAYER_MENU_LABEL,
  PLAYER_POPOVER,
  PLAYER_RANGE_OVERLAY,
  PLAYER_SHEET,
  PLAYER_SHEET_BODY,
  PLAYER_SHEET_HEADER,
  PLAYER_TEXT_BUTTON,
  PLAYER_TRACK,
  PLAYER_TRACK_FILL,
} from './playerTheme'

// PlayerControls — 底部操作栏（自绘，替代原生 <video controls>，因为原生栏放不下
// 自定义按钮）。
//
// 排版参考 B 站：进度条单独占一行贴着画面底部，下面是一排等高等宽的图标按钮；
// 只有「清晰度 / 倍速 / 选集」保留文字（这三个需要显示当前状态），其余一律
// 收敛成同尺寸图标。任何需要展开选择的内容都只有两种落点——要么是贴着操作栏
// 向上的小弹层，要么是右侧的「设置」面板，不再像以前那样在一条操作栏里混着
// 圆形药丸按钮、独立下拉菜单和整块浮层面板。
//
// VR 全景播放里 auto-hide 的规则要反过来：按住拖动就是转动视角，属于观看动作
// 而不是操作意图，所以这类鼠标/触摸移动既不唤出控制栏，还会在开始转视角时
// 立刻把浮层收掉（见 onVrPointerDown / onVrPointerMove）。

function formatTime(s: number): string {
  if (!Number.isFinite(s) || s < 0) s = 0
  const m = Math.floor(s / 60)
  const sec = Math.floor(s % 60)
  return `${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`
}

const SEEK_STEP_SEC = 10
const SEEK_REPEAT_MS = 160
const SEEK_APPLY_MS = 220
const SEEK_HINT_MS = 700
const PLAYBACK_RATE_REPEAT_MS = 140
const PLAYBACK_RATE_HINT_MS = 900
/** 播放中控制栏无操作自动隐藏的延时。 */
const CONTROLS_HIDE_DELAY_MS = 3000
/** VR 全景播放的隐藏延时更短：浮层由单击唤出，收得越快越不打扰观看。 */
const VR_CONTROLS_HIDE_DELAY_MS = 3000
/** VR 视角拖拽超过这个距离才算「真的在转动视角」，轻触不受影响。 */
const VR_VIEW_DRAG_DISTANCE = 6

/**
 * 当前设备是否没有真正的鼠标悬停能力（手机/平板）。
 *
 * 触摸设备上「悬停保持显示」没有可靠的结束信号：手指抬起后浏览器不保证补发
 * mouseleave，标记一旦置位就再也清不掉，控制栏会永久留在画面上。这里按设备
 * 能力判断，避免依赖 MouseEvent.sourceCapabilities（实测常为 null，等于没修）。
 */
function isHoverlessDevice(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return true
  return window.matchMedia('(hover: none)').matches
}

/** 把 0~1 的比例夹到安全区间，避免手柄/气泡贴到两端被裁掉。 */
function clampRatio(value: number, min = 0, max = 1): number {
  if (!Number.isFinite(value)) return min
  return Math.min(max, Math.max(min, value))
}

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
  /** 外部浮层（VR 工具条/设置面板）正被鼠标悬停：此时控制栏不自动隐藏。 */
  uiHold?: boolean
  /**
   * 画面已被锁住（播放器左侧的锁）。锁住时操作栏整条隐藏，自动隐藏计时器
   * 也不再启动——否则计时器会在用户看不见的地方继续改写可见状态。
   */
  uiLocked?: boolean
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
  /** 弹幕设置面板是否打开。 */
  danmakuOpen: boolean
  /** 弹幕当前是否渲染在画面上：操作栏的「弹」按钮用它显示开关状态。 */
  danmakuEnabled: boolean
  /**
   * 打开弹幕设置面板。弹幕的加载开关、来源搜索、渲染参数都收在这个面板里，
   * 所以「弹」按钮直接指向它——不再经过「设置」面板中转。
   */
  onOpenDanmaku?: () => void
  hasPrevEpisode?: boolean
  hasNextEpisode?: boolean
  onPrevEpisode?: () => void
  onNextEpisode?: () => void
  prevEpisodeTitle?: string
  nextEpisodeTitle?: string
  playlistOpen?: boolean
  hasPlaylist?: boolean
  /** 当前条目有多个版本：选集面板里可切换版本。 */
  hasVersions?: boolean
  onTogglePlaylist?: () => void
  qualities?: PlaybackQuality[]
  /** 清晰度按钮上显示的文字（播放页按当前播放方式算好，直连时是「原画」）。 */
  qualityLabel?: string
  selectedQuality?: string
  onSelectQuality?: (quality: PlaybackQuality) => void
  showQuality?: boolean
  /** 当前播放方式的文字描述（直接播放 / HLS 转码 / 客户端直连解码 …）。 */
  playbackModeLabel?: string
  /** 可切换播放方式时提供；不可切换（直连解码、远程挂载）时不传，面板里显示为状态。 */
  onTogglePlaybackMode?: () => void
  /** VR 全景播放配置；非 null 表示当前处于 VR 模式。 */
  vr360?: Vr360Profile | null
  /** 是否由文件名/画幅自动识别为 VR 素材（按钮上加一个小圆点提示）。 */
  vr360Detected?: boolean
  onToggleVr360?: () => void
  /** Media metadata duration (seconds). Used when HLS only knows transcoded length. */
  knownDuration?: number
  /** Absolute source offset of the current HLS session (seconds). */
  streamOffset?: number
  /** Absolute seek on the full timeline; return true when handled (e.g. HLS restart). */
  onSeekAbsolute?: (seconds: number) => boolean
  /**
   * 竖屏剧场模式：控制栏按钮放大到触控尺寸（44px 起），清晰度/倍速/
   * 选集/字幕弹层改为视频区底部的动作面板，避免右侧抽屉与悬浮小弹层
   * 在窄屏下被裁剪。桌面端保持原样。
   */
  theater?: boolean
}

export function PlayerControls({
  videoRef,
  theater = false,
  volume: volumeProp = 1,
  onVolumeChange,
  onVolumeCommit,
  playbackRate: playbackRateProp = 1,
  onPlaybackRateChange,
  onPlaybackRateCommit,
  uiVisible,
  onUiVisibleChange,
  uiHold = false,
  uiLocked = false,
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
  onOpenDanmaku,
  hasPrevEpisode = false,
  hasNextEpisode = false,
  onPrevEpisode,
  onNextEpisode,
  prevEpisodeTitle,
  nextEpisodeTitle,
  playlistOpen = false,
  hasPlaylist = false,
  hasVersions = false,
  onTogglePlaylist,
  qualities = [],
  qualityLabel = '清晰度',
  selectedQuality = '',
  onSelectQuality,
  showQuality = false,
  playbackModeLabel = '',
  onTogglePlaybackMode,
  vr360 = null,
  vr360Detected = false,
  onToggleVr360,
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
  /** 已缓冲到的绝对秒数（HLS 会话要加上 streamOffset 才是完整时间轴）。 */
  const [buffered, setBuffered] = useState(0)
  const [volume, setVolume] = useState(volumeProp)
  const [muted, setMuted] = useState(volumeProp === 0)
  const [fullscreen, setFullscreen] = useState(false)
  const [pip, setPip] = useState(false)
  const [controlsHovered, setControlsHovered] = useState(false)
  const [isScrubbing, setIsScrubbing] = useState(false)
  const [scrubValue, setScrubValue] = useState<number | null>(null)
  /** 进度条上的悬停预览位置（0~1）与对应秒数；移出即清空。 */
  const [seekHover, setSeekHover] = useState<number | null>(null)
  const [subtitleMenuOpen, setSubtitleMenuOpen] = useState(false)
  const subtitleMenuRef = useRef<HTMLDivElement | null>(null)
  const [qualityMenuOpen, setQualityMenuOpen] = useState(false)
  const qualityMenuRef = useRef<HTMLDivElement | null>(null)
  const [speedMenuOpen, setSpeedMenuOpen] = useState(false)
  const speedMenuRef = useRef<HTMLDivElement | null>(null)
  const [settingsMenuOpen, setSettingsMenuOpen] = useState(false)
  const settingsMenuRef = useRef<HTMLDivElement | null>(null)
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const isScrubbingRef = useRef(false)
  /** 控制栏当前是否处于「必须保持显示」的状态（悬停/拖进度/菜单或面板打开）。 */
  const holdControlsRef = useRef(false)
  /** 当前是否在 VR 全景播放中。 */
  const vr360ActiveRef = useRef(Boolean(vr360))
  /** 画面是否已锁住（VR 锁）。事件回调里读它，避免闭包读到过期值。 */
  const uiLockedRef = useRef(uiLocked)
  /** VR 画布上的按下状态：按住拖动 = 转动视角，不唤出控制栏。 */
  const vrDragRef = useRef<{ x: number; y: number; moved: boolean } | null>(null)
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
    isScrubbingRef.current = isScrubbing
  }, [isScrubbing])

  useEffect(() => {
    vr360ActiveRef.current = Boolean(vr360)
  }, [vr360])

  useEffect(() => {
    uiLockedRef.current = uiLocked
  }, [uiLocked])

  // 「保持显示」条件集中在这里：悬停控制栏、拖动进度条、打开任何菜单或面板。
  // 事件回调里读 ref，渲染里读派生值，两边始终一致。
  const keepControlsVisible =
    uiHold ||
    controlsHovered ||
    isScrubbing ||
    subtitleMenuOpen ||
    qualityMenuOpen ||
    speedMenuOpen ||
    settingsMenuOpen ||
    danmakuOpen ||
    playlistOpen
  useEffect(() => {
    holdControlsRef.current = keepControlsVisible
  }, [keepControlsVisible])

  // 控制栏「悬停保持显示」在触摸设备上没有可靠的结束信号：手指抬起后浏览器不保证
  // 补发 mouseleave，标记会一直挂着，控制栏就永远不隐藏。触摸设备直接不进入悬停
  // 保持状态，改用 pointerup 兜底清理，避免个别浏览器仍派发 mouseenter。
  useEffect(() => {
    const onPointerUp = (event: PointerEvent) => {
      if (event.pointerType !== 'touch') return
      setControlsHovered(false)
    }
    window.addEventListener('pointerup', onPointerUp)
    return () => window.removeEventListener('pointerup', onPointerUp)
  }, [])

  useEffect(() => {
    durationRef.current = duration
  }, [duration])

  useEffect(() => {
    playbackRateRef.current = normalizePlaybackRate(playbackRateProp)
  }, [playbackRateProp])

  useEffect(() => {
    setStageEl(container())
  }, [container])

  // 浮层收起时同步关掉所有弹层：否则用户下次唤出操作栏，上一次没点掉的菜单会
  // 直接弹回来盖住画面。
  useEffect(() => {
    if (uiVisible && !uiLocked) return
    setSubtitleMenuOpen(false)
    setQualityMenuOpen(false)
    setSpeedMenuOpen(false)
    setSettingsMenuOpen(false)
  }, [uiVisible, uiLocked])

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

  // 点击控制栏外部时关掉字幕/清晰度/倍速/设置弹层
  useEffect(() => {
    if (!subtitleMenuOpen && !qualityMenuOpen && !speedMenuOpen && !settingsMenuOpen) return
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
      if (settingsMenuRef.current && !settingsMenuRef.current.contains(e.target as Node)) {
        setSettingsMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [subtitleMenuOpen, qualityMenuOpen, speedMenuOpen, settingsMenuOpen])

  // 播放时 3 秒无操作自动隐藏控制栏；暂停/悬停/拖动进度条/打开菜单时保持显示。
  // 监听挂在整个播放器舞台容器（data-player-stage）上，避免光标移到控制栏时因离开视频画面而误触发 mouseleave。
  useEffect(() => {
    const el = video()
    const stage = container()
    if (!el || !stage) return

    const holdControls = () => holdControlsRef.current

    /** 画面锁住时，控制栏必须保持隐藏，任何「用户活动」都不应该把它唤回来。 */
    const isLocked = () => uiLockedRef.current

    const hideDelay = () =>
      vr360ActiveRef.current ? VR_CONTROLS_HIDE_DELAY_MS : CONTROLS_HIDE_DELAY_MS

    const resetTimer = () => {
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
      if (el.paused || holdControls() || isLocked()) return
      hideTimerRef.current = setTimeout(() => {
        if (holdControls()) return
        onUiVisibleChange(false)
      }, hideDelay())
    }

    // 通用「用户活动」入口：显示控制栏并重新计时。播放/暂停等事件也走这里。
    const onMove = () => {
      if (isLocked()) return
      onUiVisibleChange(true)
      resetTimer()
    }

    // VR 全景播放里鼠标/触摸移动是「转动视角」的观看动作，不是操作控制栏的意图：
    //   · 移动一律不唤出控制栏（拖动结束后的轻微抖动、手机上轻触产生的兼容
    //     mousemove 都不会让进度条又冒出来）；
    //   · 一旦判定用户真的在转视角，就立刻收起浮层，还给用户一个干净的画面。
    // 需要控制栏时用轻触/单击画面唤出（见 PlayerVideoStage 的 handleSurfaceActivate）。
    const onStageMouseMove = () => {
      if (vr360ActiveRef.current) return
      onMove()
    }

    const onVrPointerDown = (event: PointerEvent) => {
      if (!vr360ActiveRef.current) return
      const target = event.target
      if (!(target instanceof Element) || !target.closest('[data-vr360-surface]')) return
      vrDragRef.current = { x: event.clientX, y: event.clientY, moved: false }
    }

    const onVrPointerMove = (event: PointerEvent) => {
      const drag = vrDragRef.current
      if (!drag || drag.moved) return
      if (Math.hypot(event.clientX - drag.x, event.clientY - drag.y) <= VR_VIEW_DRAG_DISTANCE) return
      drag.moved = true
      if (holdControls()) return
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
      onUiVisibleChange(false)
    }

    const endVrDrag = () => {
      vrDragRef.current = null
    }

    const onLeave = (e: MouseEvent) => {
      endVrDrag()
      // 仅当光标真正移出 stage 容器时才处理
      if (e.relatedTarget && stage.contains(e.relatedTarget as Node)) {
        return
      }
      if (el.paused || holdControls()) return
      if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
      onUiVisibleChange(false)
    }

    const syncPlay = () => {
      setPlaying(!el.paused)
      onMove()
    }
    const syncTime = () => {
      const ranges = el.buffered
      if (ranges && ranges.length > 0) {
        try {
          setBuffered(streamOffset + ranges.end(ranges.length - 1))
        } catch {
          setBuffered(0)
        }
      }
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

    stage.addEventListener('mousemove', onStageMouseMove)
    stage.addEventListener('mouseleave', onLeave)
    // VR 视角拖拽：pointer 事件比 mouse 事件先到达，可以在同一次移动里
    // 先判定「这是转动视角」，避免鼠标移动先把控制栏唤出来再收掉。
    stage.addEventListener('pointerdown', onVrPointerDown)
    stage.addEventListener('pointermove', onVrPointerMove)
    stage.addEventListener('pointerup', endVrDrag)
    stage.addEventListener('pointercancel', endVrDrag)

    el.addEventListener('play', syncPlay)
    el.addEventListener('playing', syncPlay)
    el.addEventListener('pause', syncPlay)
    el.addEventListener('timeupdate', syncTime)
    el.addEventListener('progress', syncTime)
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
      stage.removeEventListener('mousemove', onStageMouseMove)
      stage.removeEventListener('mouseleave', onLeave)
      stage.removeEventListener('pointerdown', onVrPointerDown)
      stage.removeEventListener('pointermove', onVrPointerMove)
      stage.removeEventListener('pointerup', endVrDrag)
      stage.removeEventListener('pointercancel', endVrDrag)
      el.removeEventListener('play', syncPlay)
      el.removeEventListener('playing', syncPlay)
      el.removeEventListener('pause', syncPlay)
      el.removeEventListener('timeupdate', syncTime)
      el.removeEventListener('progress', syncTime)
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

  // 控制栏的自动隐藏计时器：只要控制栏处于显示状态，就始终安排一次隐藏。
  // 之前只在「悬停/菜单状态变化」时才安排，用轻触或点击唤出控制栏后如果没有
  // 鼠标移动，计时器永远不会被安排，控制栏就会一直留在画面上。
  useEffect(() => {
    if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    // 锁住时控制栏整条不可见，也没有任何计时器需要跑。
    if (uiLocked) return
    if (keepControlsVisible) {
      onUiVisibleChange(true)
      return
    }
    if (!uiVisible) return
    const el = video()
    if (!el || el.paused) return
    hideTimerRef.current = setTimeout(() => {
      if (holdControlsRef.current) return
      onUiVisibleChange(false)
    }, vr360ActiveRef.current ? VR_CONTROLS_HIDE_DELAY_MS : CONTROLS_HIDE_DELAY_MS)
  }, [uiVisible, keepControlsVisible, uiLocked, onUiVisibleChange, video])

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
    setSeekHover(null)
  }

  /** 进度条悬停预览：记录光标在轨道上的比例，用于显示秒数气泡。 */
  const handleSeekHover = (event: React.MouseEvent<HTMLDivElement>) => {
    if (event.clientX === 0 && event.clientY === 0) return
    const rect = event.currentTarget.getBoundingClientRect()
    if (rect.width <= 0) return
    setSeekHover(clampRatio((event.clientX - rect.left) / rect.width))
  }

  const revealControls = useCallback(() => {
    // 锁住时键盘操作（方向键调进度/音量）不应该把控制栏唤回来。
    if (uiLockedRef.current) return
    onUiVisibleChange(true)
    if (hideTimerRef.current) clearTimeout(hideTimerRef.current)
    const el = video()
    if (!el || el.paused || holdControlsRef.current) {
      return
    }
    hideTimerRef.current = setTimeout(() => {
      if (holdControlsRef.current) return
      onUiVisibleChange(false)
    }, vr360ActiveRef.current ? VR_CONTROLS_HIDE_DELAY_MS : CONTROLS_HIDE_DELAY_MS)
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
  const qualityGroups = [
    { key: 'cloud', label: '115 云端', items: qualities.filter((quality) => quality.source !== 'local') },
    { key: 'local', label: '本地 HLS', items: qualities.filter((quality) => quality.source === 'local') },
  ].filter((group) => group.items.length > 0)
  const hasQualityGroups = qualityGroups.length > 0

  const durationSafe = Math.max(0, duration)
  const playedRatio = durationSafe > 0 ? clampRatio(displayTime / durationSafe) : 0
  const bufferedRatio = durationSafe > 0 ? clampRatio(buffered / durationSafe) : 0
  const volumeRatio = clampRatio(muted ? 0 : volume)

  // 悬停气泡：拖动时优先跟手柄走，否则跟光标。位置夹在两端内，避免气泡被裁掉。
  const tooltipRatio = clampRatio(isScrubbing ? playedRatio : (seekHover ?? playedRatio), 0.04, 0.96)
  const tooltipVisible = seekHover !== null || isScrubbing
  const tooltipTime = isScrubbing || seekHover !== null
    ? clampRatio(isScrubbing ? playedRatio : (seekHover as number)) * durationSafe
    : 0

  const seekOverlay = seekHint && stageEl
    ? createPortal(
        <div className="pointer-events-none absolute inset-0 z-30">
          <div
            className={`absolute top-1/2 flex w-32 -translate-y-1/2 flex-col items-center justify-center rounded-2xl bg-black/55 px-3 py-4 text-white shadow-lg backdrop-blur-sm ${
              seekHint.dir === 'back' ? 'left-[8%] sm:left-[12%]' : 'right-[8%] sm:right-[12%]'
            }`}
          >
            {seekHint.dir === 'back' ? <Rewind size={26} /> : <FastForward size={26} />}
            <span className="mt-1 text-center text-xs font-medium leading-tight">
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
          <div className="flex items-center gap-2 rounded-xl bg-black/65 px-4 py-2.5 text-white shadow-lg backdrop-blur-sm">
            <Timer size={20} />
            <span className="text-xs font-semibold tabular-nums">
              {formatPlaybackRate(playbackRateHint)} 倍速
            </span>
          </div>
        </div>,
        stageEl,
      )
    : null

  /** 小屏下操作栏放不下的项目（清晰度/倍速/选集/字幕）收进「设置」面板。 */
  const compactOnlySections = (
    <>
      {showQuality && hasQualityGroups && (
        <div className={theater ? undefined : 'sm:hidden'}>
          <p className={PLAYER_MENU_LABEL}>清晰度</p>
          <QualityItems
            groups={qualityGroups}
            selectedQuality={selectedQuality}
            onSelect={(quality) => onSelectQuality?.(quality)}
          />
        </div>
      )}
      <div className={theater ? undefined : 'sm:hidden'}>
        <p className={PLAYER_MENU_LABEL}>播放速度</p>
        <SpeedItems
          playbackRate={playbackRateProp}
          onSelect={(rate) => selectPlaybackRate(rate)}
        />
      </div>
      {onTogglePlaylist && (
        <div className={theater ? undefined : 'sm:hidden'}>
          <p className={PLAYER_MENU_LABEL}>选集</p>
          <button
            type="button"
            disabled={!hasPlaylist}
            onClick={() => {
              setSettingsMenuOpen(false)
              onTogglePlaylist()
            }}
            className={`${PLAYER_MENU_ITEM} disabled:cursor-not-allowed disabled:text-white/30 disabled:hover:bg-transparent`}
          >
            <ListVideo size={14} className="shrink-0 text-white/50" />
            <span className="min-w-0 flex-1 truncate">
              {hasPlaylist ? '打开选集列表' : '当前无更多剧集'}
            </span>
            {hasPlaylist && hasVersions && <Layers size={12} className="shrink-0 text-white/40" />}
          </button>
        </div>
      )}
      {subs.length > 0 && (
        <div className={theater ? undefined : 'sm:hidden'}>
          <p className={PLAYER_MENU_LABEL}>字幕</p>
          <SubtitleItems
            subs={subs}
            subtitleIndex={subtitleIndex}
            onSelectSubtitle={onSelectSubtitle}
            canAdjustSelectedSubtitle={canAdjustSelectedSubtitle}
            subtitlePosition={subtitlePosition}
            onSubtitlePositionChange={onSubtitlePositionChange}
            subtitleStyle={subtitleStyle}
            onSubtitleStyleChange={onSubtitleStyleChange}
            usesOriginalASS={usesOriginalASS}
            canConvertSelectedSubtitle={canConvertSelectedSubtitle}
            subtitleChineseMode={subtitleChineseMode}
            onSubtitleChineseModeChange={onSubtitleChineseModeChange}
          />
        </div>
      )}
      <div className={theater ? PLAYER_MENU_DIVIDER : `sm:hidden ${PLAYER_MENU_DIVIDER}`} />
    </>
  )

  const settingsPanel = (
    <div className="p-1">
      {compactOnlySections}

      {playbackModeLabel && (
        <div>
          <p className={PLAYER_MENU_LABEL}>播放方式</p>
          {onTogglePlaybackMode ? (
            <button
              type="button"
              onClick={() => {
                setSettingsMenuOpen(false)
                onTogglePlaybackMode()
              }}
              className={PLAYER_MENU_ITEM}
              title="在直接播放与 HLS 转码之间切换"
            >
              <Rotate3d size={14} className="shrink-0 text-white/50" />
              <span className="min-w-0 flex-1 truncate">{playbackModeLabel}</span>
              <span className="shrink-0 text-[10px] text-rose-300">切换</span>
            </button>
          ) : (
            <p className="flex items-center gap-2 px-2.5 py-1.5 text-xs text-white/60">
              <Lock size={13} className="shrink-0 text-white/35" />
              <span className="min-w-0 flex-1 truncate">{playbackModeLabel}</span>
            </p>
          )}
        </div>
      )}

      {onToggleVr360 && (
        <div>
          <p className={PLAYER_MENU_LABEL}>画面</p>
          <button
            type="button"
            onClick={() => {
              setSettingsMenuOpen(false)
              onToggleVr360()
            }}
            className={PLAYER_MENU_ITEM}
            title={vr360 ? '退出 VR 全景播放' : '切到 VR 全景播放（鼠标拖动或手机陀螺仪转视角）'}
          >
            <Rotate3d size={14} className={vr360 ? 'shrink-0 text-rose-400' : 'shrink-0 text-white/50'} />
            <span className="min-w-0 flex-1 truncate">VR 全景播放</span>
            {vr360 ? (
              <Check size={13} className="shrink-0 text-rose-400" />
            ) : vr360Detected ? (
              <span className="shrink-0 text-[10px] text-amber-300">已识别</span>
            ) : null}
          </button>
        </div>
      )}
    </div>
  )

  return (
    <>
    {seekOverlay}
    {playbackRateOverlay}
    {theater && settingsMenuOpen ? (
      <div
        className="absolute inset-x-0 bottom-0 z-40 flex items-end justify-center"
        onClick={(e) => e.stopPropagation()}
        onPointerUp={(e) => e.stopPropagation()}
      >
        <div
          className="absolute inset-0"
          onClick={() => setSettingsMenuOpen(false)}
          aria-hidden
        />
        <div className={`relative ${PLAYER_SHEET}`}>
          <div className={PLAYER_SHEET_HEADER}>
            <span className="text-[13px] font-semibold text-white/90">播放器设置</span>
            <button
              type="button"
              onClick={() => setSettingsMenuOpen(false)}
              className={PLAYER_ICON_BUTTON}
              title="关闭设置"
            >
              <Minimize size={16} />
            </button>
          </div>
          <div className={PLAYER_SHEET_BODY}>{settingsPanel}</div>
        </div>
      </div>
    ) : null}
    <div
      className={`pointer-events-auto absolute inset-x-0 bottom-0 z-20 bg-gradient-to-t from-black/85 via-black/45 to-transparent px-2.5 pb-1.5 pt-12 transition-opacity duration-300 sm:px-3.5 sm:pb-2.5 ${
        uiVisible ? 'opacity-100' : 'pointer-events-none opacity-0'
      }`}
      onMouseEnter={() => {
        if (isHoverlessDevice()) return
        setControlsHovered(true)
      }}
      onMouseLeave={() => setControlsHovered(false)}
      onClick={(e) => e.stopPropagation()}
      onPointerUp={(e) => e.stopPropagation()}
    >
      {/* 进度条：单独占一行贴着画面底部，轨道只有 3px，悬停才变粗并露出圆点手柄 */}
      <div
        className={`group/seek relative flex w-full items-center ${theater ? 'h-9' : 'h-5'}`}
        onMouseMove={handleSeekHover}
        onMouseLeave={() => setSeekHover(null)}
      >
        <div className={`${PLAYER_TRACK} ${theater ? 'h-[5px]' : ''} group-hover/seek:h-[5px]`}>
          <div
            className={`${PLAYER_TRACK_FILL} bg-white/30`}
            style={{ width: `${bufferedRatio * 100}%` }}
          />
          <div
            className={`${PLAYER_TRACK_FILL} bg-rose-500`}
            style={{ width: `${playedRatio * 100}%` }}
          />
        </div>
        <div
          className={`pointer-events-none absolute h-3 w-3 -translate-x-1/2 rounded-full bg-white shadow-md transition-opacity duration-150 group-hover/seek:opacity-100 ${theater ? 'opacity-100' : 'opacity-0'}`}
          style={{ left: `${playedRatio * 100}%` }}
        />
        {tooltipVisible && durationSafe > 0 ? (
          <div
            className="pointer-events-none absolute bottom-4 -translate-x-1/2 rounded-md bg-black/85 px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-white shadow-lg"
            style={{ left: `${tooltipRatio * 100}%` }}
          >
            {formatTime(tooltipTime)}
          </div>
        ) : null}
        <input
          type="range"
          data-player-seek-range="true"
          min={0}
          max={durationSafe}
          step={0.1}
          value={displayTime}
          onMouseDown={handleSeekStart}
          onTouchStart={handleSeekStart}
          onChange={(e) => handleSeekChange(Number(e.target.value))}
          onMouseUp={(e) => handleSeekEnd(Number((e.target as HTMLInputElement).value))}
          onTouchEnd={(e) => handleSeekEnd(Number((e.target as HTMLInputElement).value))}
          className={PLAYER_RANGE_OVERLAY}
          aria-label="播放进度"
        />
      </div>

      <div className={`flex items-center ${theater ? 'mt-1 gap-1' : 'mt-0.5 gap-0.5 sm:mt-1 sm:gap-1'}`}>
        {/* 播放 / 暂停 */}
        <button
          onClick={togglePlay}
          className={`${PLAYER_ICON_BUTTON} ${theater ? 'h-11 w-11' : 'h-8 w-8 sm:h-9 sm:w-9'}`}
          title={playing ? '暂停 (Space)' : '播放 (Space)'}
        >
          {playing ? <Pause size={theater ? 24 : 19} /> : <Play size={theater ? 24 : 19} />}
        </button>

        {/* 上一集 / 下一集：单条媒体（电影）没有上下集时整组隐藏，少两个死按钮 */}
        {(hasPrevEpisode || hasNextEpisode) && (
          <>
            <button
              onClick={onPrevEpisode}
              disabled={!hasPrevEpisode}
              className={`${PLAYER_ICON_BUTTON} ${theater ? 'h-11 w-11' : ''}`}
              title={
                hasPrevEpisode
                  ? prevEpisodeTitle
                    ? `上一集：${prevEpisodeTitle} ([)`
                    : '上一集 ([)'
                  : '没有上一集'
              }
            >
              <SkipBack size={16} />
            </button>
            <button
              onClick={onNextEpisode}
              disabled={!hasNextEpisode}
              className={`${PLAYER_ICON_BUTTON} ${theater ? 'h-11 w-11' : ''}`}
              title={
                hasNextEpisode
                  ? nextEpisodeTitle
                    ? `下一集：${nextEpisodeTitle} (])`
                    : '下一集 (])'
                  : '没有下一集'
              }
            >
              <SkipForward size={16} />
            </button>
          </>
        )}

        <span className="ml-1 shrink-0 font-mono text-[11px] tabular-nums text-white/90 sm:text-xs">
          {formatTime(displayTime)} / {formatTime(duration)}
        </span>

        <div className="min-w-0 flex-1" />

        {/* 清晰度：文字按钮直接显示当前档位（B 站也是这么做的） */}
        {showQuality && hasQualityGroups && (
          <div className="relative hidden sm:block" ref={qualityMenuRef}>
            <button
              onClick={() => {
                setSubtitleMenuOpen(false)
                setSpeedMenuOpen(false)
                setSettingsMenuOpen(false)
                setQualityMenuOpen((v) => !v)
              }}
              className={`${PLAYER_TEXT_BUTTON} max-w-[7rem] ${qualityMenuOpen ? 'bg-white/15 text-white' : ''}`}
              title="清晰度"
            >
              <span className="truncate">{qualityLabel}</span>
            </button>
            {qualityMenuOpen && (
              <div className={`${PLAYER_POPOVER} absolute bottom-11 right-0 min-w-[11rem]`}>
                <QualityItems
                  groups={qualityGroups}
                  selectedQuality={selectedQuality}
                  onSelect={(quality) => {
                    onSelectQuality?.(quality)
                    setQualityMenuOpen(false)
                  }}
                />
              </div>
            )}
          </div>
        )}

        {/* 倍速 */}
        <div className="relative hidden sm:block" ref={speedMenuRef}>
          <button
            onClick={() => {
              setSubtitleMenuOpen(false)
              setQualityMenuOpen(false)
              setSettingsMenuOpen(false)
              setSpeedMenuOpen((v) => !v)
            }}
            className={`${PLAYER_TEXT_BUTTON} ${speedMenuOpen ? 'bg-white/15 text-white' : ''}`}
            title="播放速度（↑/↓ 调节）"
          >
            {formatPlaybackRate(playbackRateProp)}
          </button>
          {speedMenuOpen && (
            <div className={`${PLAYER_POPOVER} absolute bottom-11 right-0 min-w-[9rem]`}>
              <p className={PLAYER_MENU_LABEL}>播放速度</p>
              <SpeedItems
                playbackRate={playbackRateProp}
                onSelect={(rate) => {
                  selectPlaybackRate(rate)
                  setSpeedMenuOpen(false)
                }}
              />
            </div>
          )}
        </div>

        {/* 选集 / 版本切换（多版本条目也在这里换） */}
        {onTogglePlaylist && (
          <button
            onClick={onTogglePlaylist}
            disabled={!hasPlaylist}
            className={`${PLAYER_TEXT_BUTTON} hidden sm:flex ${
              playlistOpen ? 'bg-rose-500/90 text-white hover:bg-rose-500' : ''
            }`}
            title={
              !hasPlaylist ? '当前无更多剧集' : hasVersions ? '选集与版本切换' : '选集列表'
            }
          >
            <ListVideo size={15} />
            <span>选集</span>
            {hasPlaylist && hasVersions && (
              <span className="flex items-center gap-0.5 rounded-full bg-black/25 px-1 py-px text-[9px] leading-none">
                <Layers size={9} />
                版本
              </span>
            )}
          </button>
        )}

        {/* 弹幕：点开就是弹幕设置面板。弹幕的加载开关、来源搜索、渲染参数都在
            那个面板里，所以这里不再做「先切设置再找弹幕」的中转；按钮上的方块
            颜色仍然反映当前弹幕是否显示。 */}
        <button
          type="button"
          onClick={onOpenDanmaku}
          disabled={!onOpenDanmaku}
          className={`${PLAYER_ICON_BUTTON} ${theater ? 'h-11 w-11' : ''} ${danmakuOpen ? 'bg-white/15' : ''}`}
          title={danmakuOpen ? '弹幕设置（已打开）' : '弹幕设置'}
        >
          <span
            className={`flex h-[18px] w-[18px] items-center justify-center rounded-[4px] text-[10px] font-bold leading-none transition ${
              danmakuEnabled
                ? 'bg-rose-500 text-white'
                : 'border border-white/40 text-white/45'
            }`}
          >
            弹
          </span>
        </button>

        {/* 音量：图标管静音，悬停展开滑条 */}
        <div className="group/volume flex shrink-0 items-center">
          <button
            onClick={toggleMute}
            className={PLAYER_ICON_BUTTON}
            title={muted || volume === 0 ? '取消静音 (M)' : '静音 (M)'}
          >
            {muted || volume === 0 ? <VolumeX size={17} /> : <Volume2 size={17} />}
          </button>
          <div className="hidden w-0 overflow-hidden transition-[width] duration-200 ease-out group-hover/volume:w-[76px] group-focus-within/volume:w-[76px] sm:block">
            <div className="relative mx-1.5 flex h-4 w-16 items-center">
              <div className={`${PLAYER_TRACK} h-[3px]`}>
                <div
                  className={`${PLAYER_TRACK_FILL} bg-white`}
                  style={{ width: `${volumeRatio * 100}%` }}
                />
              </div>
              <div
                className="pointer-events-none absolute h-3 w-3 -translate-x-1/2 rounded-full bg-white opacity-0 shadow-md transition-opacity group-hover/volume:opacity-100"
                style={{ left: `${volumeRatio * 100}%` }}
              />
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
                className={PLAYER_RANGE_OVERLAY}
                aria-label="音量"
              />
            </div>
          </div>
        </div>

        {/* 字幕 */}
        {subs.length > 0 && (
          <div className="relative hidden sm:block" ref={subtitleMenuRef}>
            <button
              onClick={() => {
                setQualityMenuOpen(false)
                setSpeedMenuOpen(false)
                setSettingsMenuOpen(false)
                setSubtitleMenuOpen((v) => !v)
              }}
              className={`${PLAYER_ICON_BUTTON} ${subtitleMenuOpen ? 'bg-white/15 text-white' : ''}`}
              title="字幕"
            >
              {subtitleIndex >= 0 ? (
                <Captions size={17} className="text-rose-400" />
              ) : (
                <CaptionsOff size={17} />
              )}
            </button>
            {subtitleMenuOpen && (
              <div className={`${PLAYER_POPOVER} absolute bottom-11 right-0 max-h-[70vh] min-w-[12rem] overflow-y-auto`}>
                <SubtitleItems
                  subs={subs}
                  subtitleIndex={subtitleIndex}
                  onSelectSubtitle={(index) => {
                    onSelectSubtitle(index)
                    setSubtitleMenuOpen(false)
                  }}
                  canAdjustSelectedSubtitle={canAdjustSelectedSubtitle}
                  subtitlePosition={subtitlePosition}
                  onSubtitlePositionChange={onSubtitlePositionChange}
                  subtitleStyle={subtitleStyle}
                  onSubtitleStyleChange={onSubtitleStyleChange}
                  usesOriginalASS={usesOriginalASS}
                  canConvertSelectedSubtitle={canConvertSelectedSubtitle}
                  subtitleChineseMode={subtitleChineseMode}
                  onSubtitleChineseModeChange={(mode) => {
                    onSubtitleChineseModeChange(mode)
                    setSubtitleMenuOpen(false)
                  }}
                />
              </div>
            )}
          </div>
        )}

        {/* 设置：播放方式 / VR / 弹幕设置，小屏下再收进清晰度、倍速、选集、字幕 */}
        <div className="relative" ref={settingsMenuRef}>
          <button
            onClick={() => {
              setQualityMenuOpen(false)
              setSpeedMenuOpen(false)
              setSubtitleMenuOpen(false)
              setSettingsMenuOpen((v) => !v)
            }}
            className={`${PLAYER_ICON_BUTTON} ${theater ? 'h-11 w-11' : ''} ${settingsMenuOpen ? 'bg-white/15 text-white' : ''}`}
            title="播放器设置"
          >
            <Settings2 size={theater ? 21 : 17} />
          </button>
          {/* 识别到 VR 素材但还没进 VR 时，用一个小圆点提示设置里有东西可开 */}
          {vr360Detected && !vr360 && (
            <span className="pointer-events-none absolute right-0.5 top-0.5 h-1.5 w-1.5 rounded-full bg-amber-300" />
          )}
          {settingsMenuOpen && !theater && (
            <div className={`${PLAYER_POPOVER} absolute bottom-11 right-0 max-h-[70vh] w-[15rem] overflow-y-auto`}>
              {settingsPanel}
            </div>
          )}
        </div>

        {pipSupported && (
          <button
            onClick={togglePip}
            className={`${PLAYER_ICON_BUTTON} hidden sm:flex`}
            title={pip ? '退出画中画' : '画中画'}
          >
            <PictureInPicture size={17} className={pip ? 'text-rose-400' : ''} />
          </button>
        )}

        <button
          onClick={toggleFullscreen}
          className={`${PLAYER_ICON_BUTTON} ${theater ? 'h-11 w-11' : ''}`}
          title={fullscreen ? '退出全屏 (F)' : '全屏 (F)'}
        >
          {fullscreen ? <Minimize size={theater ? 21 : 17} /> : <Maximize size={theater ? 21 : 17} />}
        </button>
      </div>
    </div>
    </>
  )
}

/** 清晰度选项：115 云端与本地 HLS 分组展示，转码中的档位带等待标记。 */
function QualityItems({
  groups,
  selectedQuality,
  onSelect,
}: {
  groups: { key: string; label: string; items: PlaybackQuality[] }[]
  selectedQuality: string
  onSelect: (quality: PlaybackQuality) => void
}) {
  return (
    <>
      {groups.map((group) => (
        <div key={group.key}>
          <p className={PLAYER_MENU_LABEL}>{group.label}</p>
          {group.items.map((quality) => {
            const current = quality.id === selectedQuality
            return (
              <button
                key={`${quality.source}-${quality.id}`}
                type="button"
                onClick={() => onSelect(quality)}
                className={`${PLAYER_MENU_ITEM} ${current ? 'text-rose-400' : ''}`}
                title={quality.note || quality.label}
              >
                <span className="min-w-0 flex-1 truncate">{quality.label}</span>
                {quality.requires_transcode && (
                  <span className="flex shrink-0 items-center gap-1 rounded bg-white/10 px-1.5 py-0.5 text-[9px] text-amber-300">
                    <Loader2 size={10} />
                    转码
                  </span>
                )}
                {quality.requires_vip && <Lock size={11} className="shrink-0 text-amber-300" />}
                {current && !quality.requires_transcode && (
                  <Check size={13} className="shrink-0 text-rose-400" />
                )}
              </button>
            )
          })}
        </div>
      ))}
    </>
  )
}

function SpeedItems({
  playbackRate,
  onSelect,
}: {
  playbackRate: number
  onSelect: (rate: number) => void
}) {
  return (
    <>
      {PLAYBACK_RATE_OPTIONS.map((rate) => {
        const current = normalizePlaybackRate(playbackRate) === rate
        return (
          <button
            key={rate}
            type="button"
            onClick={() => onSelect(rate)}
            className={`${PLAYER_MENU_ITEM} ${current ? 'text-rose-400' : ''}`}
          >
            <span className="min-w-0 flex-1 tabular-nums">{formatPlaybackRate(rate)}</span>
            {current && <Check size={13} className="shrink-0 text-rose-400" />}
          </button>
        )
      })}
    </>
  )
}

type SubtitleItemsProps = {
  subs: SubtitleTrack[]
  subtitleIndex: number
  onSelectSubtitle: (index: number) => void
  canAdjustSelectedSubtitle: boolean
  subtitlePosition: SubtitlePosition
  onSubtitlePositionChange: (position: SubtitlePosition) => void
  subtitleStyle: SubtitleStylePreset
  onSubtitleStyleChange: (style: SubtitleStylePreset) => void
  usesOriginalASS: boolean
  canConvertSelectedSubtitle: boolean
  subtitleChineseMode: SubtitleChineseMode
  onSubtitleChineseModeChange: (mode: SubtitleChineseMode) => void
}

/** 字幕轨道列表 + 位置/样式/简繁设置。桌面端弹层与小屏设置面板共用同一份。 */
function SubtitleItems({
  subs,
  subtitleIndex,
  onSelectSubtitle,
  canAdjustSelectedSubtitle,
  subtitlePosition,
  onSubtitlePositionChange,
  subtitleStyle,
  onSubtitleStyleChange,
  usesOriginalASS,
  canConvertSelectedSubtitle,
  subtitleChineseMode,
  onSubtitleChineseModeChange,
}: SubtitleItemsProps) {
  return (
    <>
      <button
        type="button"
        onClick={() => onSelectSubtitle(-1)}
        className={`${PLAYER_MENU_ITEM} ${subtitleIndex < 0 ? 'text-rose-400' : ''}`}
      >
        <span className="min-w-0 flex-1">关闭字幕</span>
        {subtitleIndex < 0 && <Check size={13} className="shrink-0 text-rose-400" />}
      </button>
      {subs.map((track, index) => {
        const current = subtitleIndex === index
        return (
          <button
            key={track.path}
            type="button"
            onClick={() => onSelectSubtitle(index)}
            className={`${PLAYER_MENU_ITEM} ${current ? 'text-rose-400' : ''}`}
            title={track.label || track.lang}
          >
            <span className="min-w-0 flex-1 truncate">
              {track.label || track.lang || `字幕 ${index + 1}`}
              {track.delivery === 'ass' ? ' · ASS' : ''}
            </span>
            {current && <Check size={13} className="shrink-0 text-rose-400" />}
          </button>
        )
      })}

      {canAdjustSelectedSubtitle && (
        <>
          <div className={PLAYER_MENU_DIVIDER} />
          <p className={PLAYER_MENU_LABEL}>字幕位置</p>
          <div className="flex flex-wrap gap-1 px-2.5 pb-1">
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
          <p className={PLAYER_MENU_LABEL}>字幕样式</p>
          <div className="flex flex-wrap gap-1 px-2.5 pb-1">
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
        </>
      )}

      {usesOriginalASS && (
        <p className="mx-2.5 mt-1 border-t border-white/10 px-0.5 pt-2 text-[10px] leading-relaxed text-white/45">
          ASS/SSA 使用字幕文件自带的样式与位置，不做统一覆盖。
        </p>
      )}

      {canConvertSelectedSubtitle && (
        <>
          <div className={PLAYER_MENU_DIVIDER} />
          <p className={PLAYER_MENU_LABEL}>外挂字幕简繁转换</p>
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
              onClick={() => onSubtitleChineseModeChange(mode)}
              className={`${PLAYER_MENU_ITEM} ${subtitleChineseMode === mode ? 'text-rose-400' : ''}`}
            >
              <span className="min-w-0 flex-1">{label}</span>
              {subtitleChineseMode === mode && (
                <Check size={13} className="shrink-0 text-rose-400" />
              )}
            </button>
          ))}
        </>
      )}
    </>
  )
}
