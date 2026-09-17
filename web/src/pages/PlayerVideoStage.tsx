import { useCallback, useEffect, useRef, useState } from 'react'
import type { CSSProperties, PointerEvent, ReactNode, RefObject } from 'react'
import {
  CircleAlert,
  Hand,
  Loader2,
  Lock,
  LockOpen,
  MousePointerClick,
  Rotate3d,
  Smartphone,
  ZoomIn,
} from 'lucide-react'

import { subtitlesAPI, type SubtitleTrack } from '../api/subtitles'
import { type DanmakuAnime, type DanmakuLoadedInfo } from '../api/danmaku'
import type { Media, PlaybackQuality } from '../types'
import {
  loadSubtitleChineseConverter,
  type SubtitleChineseMode,
} from '../utils/subtitleChinese'
import { AssSubtitleStage } from '../components/AssSubtitleStage'
import { DanmakuStage } from '../components/DanmakuStage'
import { PlayerControls } from '../components/PlayerControls'
import { Vr360Stage } from '../components/Vr360Stage'
import type { Vr360Profile } from '../utils/vr360'
import {
  subtitleTextStyle,
  type SubtitlePosition,
  type SubtitleStylePreset,
} from '../utils/subtitleDisplay'
import { isPointerInLockZone } from '../utils/playerLockZone'
import { parseWebVTTCues, type SubtitleCue } from '../utils/subtitleVTT'

type SubtitleRenderGroup = {
  key: string
  text: string
  style: CSSProperties
}

/**
 * 锁按钮每次被唤出后停留的时间（毫秒）。
 *
 * 锁本身默认隐身，只在「鼠标悬停感应区」或「点击画面」之后露一会儿。解锁的瞬间
 * 操作栏会立刻消失，如果锁也一起消失，用户就不知道锁在哪、也没法再锁回去。
 */
const LOCK_REVEAL_MS = 2600
/** 首次操作说明的最长停留时间：到这里仍未点「知道了」也自动收起并记录为已看过。 */
const VR_GUIDE_AUTO_DISMISS_MS = 20_000

function uniqueSubtitleCues(cues: SubtitleCue[]): SubtitleCue[] {
  const seen = new Set<string>()
  const unique: SubtitleCue[] = []
  for (const cue of cues) {
    const key = `${cue.startTime}\u0000${cue.endTime}\u0000${JSON.stringify(cue.settings)}\u0000${cue.text}`
    if (seen.has(key)) continue
    seen.add(key)
    unique.push(cue)
  }
  // WebVTT can contain more than two simultaneous tracks after ASS conversion.
  // Keep the last two visual blocks, matching the previous bilingual behavior.
  return unique.slice(-2)
}

function parsePercent(value: string | undefined): number | null {
  if (!value || !value.endsWith('%')) return null
  const parsed = Number.parseFloat(value)
  return Number.isFinite(parsed) ? Math.min(100, Math.max(0, parsed)) : null
}

function cueLineBottom(value: string | undefined): number | null {
  const percent = parsePercent(value)
  if (percent !== null) return Math.min(92, Math.max(2, 100 - percent))
  if (value === undefined) return null
  const line = Number.parseFloat(value)
  if (!Number.isFinite(line)) return null
  if (line < 0) return Math.min(92, Math.max(2, 6 - Math.abs(line) * 2))
  return Math.min(92, Math.max(2, 7 + line * 8))
}

function cueTextAlign(cue: SubtitleCue): CSSProperties['textAlign'] {
  const align = cue.settings.align
  if (align === 'left' || align === 'start') return 'left'
  if (align === 'right' || align === 'end') return 'right'
  return 'center'
}

function cuePlacementStyle(cue: SubtitleCue, position: SubtitlePosition): CSSProperties {
  const style: CSSProperties = {
    maxWidth: `${Math.min(cue.settings.size ?? 92, 94)}%`,
    textAlign: cueTextAlign(cue),
  }

  if (cue.settings.vertical) {
    style.writingMode = cue.settings.vertical === 'lr' ? 'vertical-lr' : 'vertical-rl'
  }

  if (position !== 'auto') {
    style.left = '50%'
    style.textAlign = 'center'
    if (position === 'bottom') {
      style.bottom = '5%'
      style.transform = 'translateX(-50%)'
    } else if (position === 'lower') {
      style.bottom = '13%'
      style.transform = 'translateX(-50%)'
    } else if (position === 'middle') {
      style.top = '50%'
      style.transform = 'translate(-50%, -50%)'
    } else {
      style.top = '7%'
      style.transform = 'translateX(-50%)'
    }
    return style
  }

  const explicitPosition = cue.settings.position
  const align = cue.settings.align
  if (explicitPosition !== undefined) {
    if (align === 'end' || align === 'right') {
      style.right = `${100 - explicitPosition}%`
    } else {
      style.left = `${explicitPosition}%`
      if (align !== 'start' && align !== 'left') style.transform = 'translateX(-50%)'
    }
  } else {
    style.left = '50%'
    style.transform = 'translateX(-50%)'
  }

  const bottom = cueLineBottom(cue.settings.line)
  if (bottom !== null) style.bottom = `${bottom}%`
  else style.bottom = '7%'
  return style
}

function buildSubtitleRenderGroups(
  cues: SubtitleCue[],
  position: SubtitlePosition,
): SubtitleRenderGroup[] {
  const groups = new Map<string, SubtitleRenderGroup>()
  for (const cue of cues) {
    const style = cuePlacementStyle(cue, position)
    const key = JSON.stringify(style)
    const existing = groups.get(key)
    if (existing) {
      if (!existing.text.split('\n').includes(cue.text)) {
        existing.text += `\n${cue.text}`
      }
      continue
    }
    groups.set(key, { key, text: cue.text, style })
  }
  return [...groups.values()]
}

type PlayerVideoStageProps = {
  media: Media | null
  /** 媒体元数据加载失败提示（非空时替代「加载中」展示）。 */
  loadError?: string
  playerError: string
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
  videoRef: RefObject<HTMLVideoElement>
  onVideoError: () => void
  playerVolume: number
  onPlayerVolumeChange: (volume: number) => void
  onPlayerVolumeCommit: (volume: number) => void
  playerPlaybackRate: number
  onPlayerPlaybackRateChange: (rate: number) => void
  onPlayerPlaybackRateCommit: (rate: number) => void
  danmakuEnabled: boolean
  danmakuOpacity: number
  danmakuFontSize: number
  danmakuArea: number
  danmakuSearch: string | null
  danmakuEpisodeId: number | string | null
  danmakuSearchTrigger?: number
  danmakuOpen: boolean
  /** 打开弹幕设置面板（搜索弹幕库、调整渲染参数）。 */
  onOpenDanmaku: () => void
  /** 操作栏上的「弹」开关：只控制画面上的弹幕是否渲染。 */
  onToggleDanmakuEnabled: (next: boolean) => void
  onDanmakuLoaded: (info: DanmakuLoadedInfo | null) => void
  onDanmakuCandidates: (candidates: DanmakuAnime[]) => void
  onDanmakuAlternatives: (alternatives: DanmakuAnime[]) => void
  /** Danmaku settings panel; rendered inside the stage so it stays visible in fullscreen. */
  danmakuPanel: ReactNode
  /** Playlist drawer / panel; rendered inside the stage so it stays visible in fullscreen. */
  playlistPanel?: ReactNode
  hasPrevEpisode?: boolean
  hasNextEpisode?: boolean
  onPrevEpisode?: () => void
  onNextEpisode?: () => void
  prevEpisodeTitle?: string
  nextEpisodeTitle?: string
  playlistOpen?: boolean
  hasPlaylist?: boolean
  /** 当前条目存在多个版本（选集面板里提供版本切换，按钮提示随之变化）。 */
  hasVersions?: boolean
  onTogglePlaylist?: () => void
  knownDuration?: number
  streamOffset?: number
  onSeekAbsolute?: (seconds: number) => boolean
  qualities?: PlaybackQuality[]
  /** 清晰度按钮上显示的文字（播放页按当前播放方式算好）。 */
  qualityLabel?: string
  selectedQuality?: string
  onSelectQuality?: (quality: PlaybackQuality) => void
  showQuality?: boolean
  /** 当前播放方式的文字描述，展示在设置面板里。 */
  playbackModeLabel?: string
  /** 可切换播放方式时提供；不提供则设置面板里只显示状态。 */
  onTogglePlaybackMode?: () => void
  /** VR 全景播放配置；非 null 表示当前处于 VR 模式。 */
  vr360?: Vr360Profile | null
  /** 是否由文件名/画幅自动识别出 VR 素材（用于在菜单里提示）。 */
  vr360Detected?: boolean
  onToggleVr360?: () => void
  onVr360ProfileChange?: (profile: Vr360Profile) => void
  onVr360Error?: (message: string) => void
  /** 是否展示 VR 全景播放的首次操作说明（按用户只弹一次）。 */
  showVr360Guide?: boolean
  onDismissVr360Guide?: () => void
  waiting?: boolean
  waitingMessage?: string
}

export function PlayerVideoStage({
  media,
  loadError,
  playerError,
  subs,
  subtitleIndex,
  onSelectSubtitle,
  subtitleChineseMode,
  onSubtitleChineseModeChange,
  subtitlePosition,
  onSubtitlePositionChange,
  subtitleStyle,
  onSubtitleStyleChange,
  videoRef,
  onVideoError,
  playerVolume,
  onPlayerVolumeChange,
  onPlayerVolumeCommit,
  playerPlaybackRate,
  onPlayerPlaybackRateChange,
  onPlayerPlaybackRateCommit,
  danmakuEnabled,
  danmakuOpacity,
  danmakuFontSize,
  danmakuArea,
  danmakuSearch,
  danmakuEpisodeId,
  danmakuSearchTrigger = 0,
  danmakuOpen,
  onOpenDanmaku,
  onToggleDanmakuEnabled,
  onDanmakuLoaded,
  onDanmakuCandidates,
  onDanmakuAlternatives,
  danmakuPanel,
  playlistPanel,
  hasPrevEpisode,
  hasNextEpisode,
  onPrevEpisode,
  onNextEpisode,
  prevEpisodeTitle,
  nextEpisodeTitle,
  playlistOpen,
  hasPlaylist,
  hasVersions = false,
  onTogglePlaylist,
  knownDuration,
  streamOffset,
  onSeekAbsolute,
  qualities = [],
  qualityLabel = '',
  selectedQuality = '',
  onSelectQuality,
  showQuality = false,
  playbackModeLabel = '',
  onTogglePlaybackMode,
  vr360 = null,
  vr360Detected = false,
  onToggleVr360,
  onVr360ProfileChange,
  onVr360Error,
  showVr360Guide = false,
  onDismissVr360Guide,
  waiting = false,
  waitingMessage = '',
}: PlayerVideoStageProps) {
  const stageRef = useRef<HTMLDivElement>(null)
  const [videoRatio, setVideoRatio] = useState<number | null>(null)
  const [stageRect, setStageRect] = useState<{ width: number; height: number } | null>(null)
  const [controlsVisible, setControlsVisible] = useState(true)
  // 鼠标是否停在 VR 工具条/设置面板上：停住时控制栏不自动隐藏，否则设置面板会在点击前消失。
  const [vrUiHovered, setVrUiHovered] = useState(false)
  // 画面是否被「锁住」：锁上之后操作栏、VR 工具条、进度框全部隐藏，只剩干净的
  // 画面，轻触画面也不会把它们唤回来，也不会误触发播放/暂停；想看操作栏必须先
  // 点左边的锁解锁。VR 和普通播放共用这一套状态。
  const [locked, setLocked] = useState(false)
  // 锁按钮本身默认隐身（见下方的 lockVisible），这三个状态决定它什么时候露面：
  //   lockHovered —— 桌面鼠标停在感应区里（移开就收）；
  //   lockShown   —— 点了画面之后的临时显示窗口；
  //   lockHint    —— 刚解锁，顺带显示「已解锁」提示。
  const [lockHovered, setLockHovered] = useState(false)
  const [lockShown, setLockShown] = useState(false)
  const [lockHint, setLockHint] = useState(false)
  const lockRevealTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lockVisible = lockHovered || lockShown
  const vrActive = Boolean(vr360)
  // VR 渲染器是否已经画出第一帧（在此之前给出「正在启动」提示，避免只看到黑屏）。
  // 只按「媒体 ID」记录：切换投影方式/画幅布局不会重建画布，也就不能清掉这个
  // 状态——渲染器只在挂载后上报一次就绪，清掉之后提示会一直停在「正在启动」。
  const [vrReadyMediaId, setVrReadyMediaId] = useState<string | null>(null)
  const vrReady = Boolean(media && vrReadyMediaId === media.id)
  const revealControlsOnlyRef = useRef(false)
  // 当前展示的字幕文本（由自定义字幕层渲染，100% 透明无黑框）
  const [activeCues, setActiveCues] = useState<SubtitleCue[]>([])
  const [subtitleChineseConverter, setSubtitleChineseConverter] =
    useState<(text: string) => string>(() => (text: string) => text)
  // 独立保存完整 WebVTT 时间轴。HLS seek 会替换媒体源，Chromium 此时可能清空
  // <track>.track.cues；独立时间轴不受 MediaSource 重挂载和轨道 mode 切换影响。
  const [subtitleTimeline, setSubtitleTimeline] = useState<{
    path: string
    cues: SubtitleCue[]
  } | null>(null)
  // 直连 302 尚未完成时插入 <track> 会中断加载并误报播放失败；等 canplay 再挂。
  const [tracksArmed, setTracksArmed] = useState(false)
  // libass/WASM is optional at runtime. If it fails, ASS tracks fall back to
  // the server's WebVTT conversion so playback still has visible subtitles.
  const [assFallbackPath, setAssFallbackPath] = useState<string | null>(null)
  const activeSubtitleTrack = subtitleIndex >= 0 ? subs[subtitleIndex] : undefined

  useEffect(() => {
    setAssFallbackPath(null)
  }, [media?.id, activeSubtitleTrack?.path, subs])

  useEffect(() => {
    const selectedTrack = subs[subtitleIndex]
    let cancelled = false

    setSubtitleChineseConverter(() => (text: string) => text)
    if (
      subtitleChineseMode === 'original' ||
      selectedTrack?.source !== 'external' ||
      selectedTrack.delivery !== 'webvtt'
    ) {
      return
    }

    void loadSubtitleChineseConverter(subtitleChineseMode)
      .then((converter) => {
        if (!cancelled) setSubtitleChineseConverter(() => converter)
      })
      .catch(() => {
        // 字典分包加载失败时保留原文，避免影响字幕正常显示。
      })

    return () => {
      cancelled = true
    }
  }, [subs, subtitleIndex, subtitleChineseMode])

  useEffect(() => {
    setTracksArmed(false)
    const video = videoRef.current
    if (!video || !media?.id) return
    const arm = () => setTracksArmed(true)
    if (video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
      arm()
      return
    }
    video.addEventListener('canplay', arm)
    video.addEventListener('playing', arm)
    return () => {
      video.removeEventListener('canplay', arm)
      video.removeEventListener('playing', arm)
    }
  }, [media?.id, videoRef])

  useEffect(() => {
    const selectedTrack = subs[subtitleIndex]
    const useWebVTT =
      selectedTrack?.delivery === 'webvtt' ||
      (selectedTrack?.delivery === 'ass' && assFallbackPath === selectedTrack.path)
    if (!media || subtitleIndex < 0 || !selectedTrack || !useWebVTT) {
      setSubtitleTimeline(null)
      return
    }

    const controller = new AbortController()
    setSubtitleTimeline(null)
    fetch(subtitlesAPI.url(media.id, selectedTrack.path), { signal: controller.signal })
      .then((response) => {
        if (!response.ok) throw new Error(`subtitle request failed: ${response.status}`)
        return response.text()
      })
      .then((body) => {
        setSubtitleTimeline({
          path: selectedTrack.path,
          cues: parseWebVTTCues(body),
        })
      })
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === 'AbortError')) {
          // 保留原生 TextTrack 作为请求失败时的降级路径。
          setSubtitleTimeline(null)
        }
      })

    return () => controller.abort()
  }, [assFallbackPath, media, subs, subtitleIndex])

  // 监听舞台容器的真实尺寸（响应窗口大小调整和全屏切换）
  useEffect(() => {
    const stage = stageRef.current
    if (!stage) return
    const ro = new ResizeObserver((entries) => {
      const entry = entries[0]
      if (entry) {
        setStageRect({
          width: entry.contentRect.width,
          height: entry.contentRect.height,
        })
      }
    })
    ro.observe(stage)
    return () => ro.disconnect()
  }, [])

  // 监听视频元数据加载，获取真实画面宽高比
  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    const updateRatio = () => {
      if (video.videoWidth && video.videoHeight) {
        setVideoRatio(video.videoWidth / video.videoHeight)
      }
    }
    updateRatio()
    video.addEventListener('loadedmetadata', updateRatio)
    video.addEventListener('resize', updateRatio)
    return () => {
      video.removeEventListener('loadedmetadata', updateRatio)
      video.removeEventListener('resize', updateRatio)
    }
  }, [videoRef, media])

  // 桌面端点击直接切换播放；移动端控制栏隐藏时首次轻触只唤出控制栏，
  // 控制栏已显示时再次轻触才切换播放/暂停。
  const togglePlay = () => {
    const video = videoRef.current
    if (!video) return
    if (video.paused) void video.play()?.catch(() => undefined)
    else video.pause()
  }
  /**
   * 临时把锁按钮唤出来（移动端点击画面、以及刚解锁时）。
   *
   * 每次都重新计时：连续点击时不会因为第一次的计时器到点而提前隐身。
   */
  const revealLock = useCallback((withUnlockHint: boolean) => {
    setLockShown(true)
    setLockHint(withUnlockHint)
    if (lockRevealTimerRef.current) clearTimeout(lockRevealTimerRef.current)
    lockRevealTimerRef.current = setTimeout(() => {
      lockRevealTimerRef.current = null
      setLockShown(false)
      setLockHint(false)
    }, LOCK_REVEAL_MS)
  }, [])

  useEffect(
    () => () => {
      if (lockRevealTimerRef.current) clearTimeout(lockRevealTimerRef.current)
    },
    [],
  )

  const handleStagePointerDown = (event: PointerEvent<HTMLDivElement>) => {
    // 触摸端没有悬停，靠点击画面把锁唤出来（顺带给刚上锁的用户一个解锁入口）。
    if (event.pointerType === 'touch') revealLock(false)
    // 锁住时轻触画面不做任何事；VR 模式下也不负责唤出控制栏（画面默认是干净的）。
    if (locked) {
      revealControlsOnlyRef.current = false
      return
    }
    // 未锁的移动端：控制栏隐藏时首次轻触只唤出控制栏，不切换播放。
    revealControlsOnlyRef.current =
      !vrActive && event.pointerType === 'touch' && !controlsVisible
  }

  // 桌面端：鼠标移到画面左侧中间就把锁露出来，移开就收回去。
  const handleStagePointerMove = (event: PointerEvent<HTMLDivElement>) => {
    if (event.pointerType !== 'mouse') return
    const rect = event.currentTarget.getBoundingClientRect()
    setLockHovered(
      isPointerInLockZone(event.clientX - rect.left, event.clientY - rect.top, rect.height),
    )
  }

  // 舞台任意位置的「激活」（轻触/单击）：
  //   普通播放：桌面端直接切换播放/暂停；移动端控制栏隐藏时首次轻触只唤出控制栏，
  //             控制栏已显示时再次轻触才切换播放/暂停。
  //   VR 模式：单击只负责显示/收起操作栏，不切换播放。
  // 两种情况都在锁住时完全无反应——只能点锁解锁（锁由上面的 pointerdown 唤出）。
  const handleSurfaceActivate = () => {
    if (locked) return
    if (revealControlsOnlyRef.current) {
      revealControlsOnlyRef.current = false
      setControlsVisible(true)
      return
    }
    revealControlsOnlyRef.current = false
    if (vrActive) {
      setControlsVisible((visible) => !visible)
      return
    }
    togglePlay()
  }

  const toggleLock = () => {
    const next = !locked
    setLocked(next)
    // 锁上时立刻收起操作栏；解锁时也保持收起，这样画面在任何一侧都不会突然
    // 弹出一整条浮层。解锁那一下顺带显示「已解锁」提示。
    setControlsVisible(false)
    revealLock(!next)
  }

  /** 复位锁定状态：切模式/换片时调用，避免上一次的锁挡住新的内容。 */
  const resetLock = useCallback(() => {
    setLocked(false)
    setLockShown(false)
    setLockHint(false)
    setLockHovered(false)
    if (lockRevealTimerRef.current) {
      clearTimeout(lockRevealTimerRef.current)
      lockRevealTimerRef.current = null
    }
  }, [])

  // 进入 VR 全景播放时先亮一次操作栏与 VR 工具条，让用户知道入口在哪；
  // 退出 VR 时清掉 VR 浮层的悬停状态，否则控制栏会以为还有浮层被悬停而不隐藏。
  // 两种情况都要解除锁定：切模式是个明确的操作意图，不该被上一次的锁挡住。
  useEffect(() => {
    resetLock()
    if (vrActive) {
      setControlsVisible(true)
      return
    }
    setVrUiHovered(false)
  }, [resetLock, vrActive])

  // 换集/换片时同样复位：新内容应该从干净的、可操作的状态开始。
  useEffect(() => {
    resetLock()
  }, [media?.id, resetLock])

  // 首次操作说明只出现一次：读完了点「知道了」会立即落库；放着不管也会在
  // VR_GUIDE_AUTO_DISMISS_MS 之后自动收起并同样记录为已看过，避免每次进 VR 都弹。
  const vrGuideVisible = vrActive && vrReady && showVr360Guide
  useEffect(() => {
    if (!vrGuideVisible) return
    const timer = setTimeout(() => onDismissVr360Guide?.(), VR_GUIDE_AUTO_DISMISS_MS)
    return () => clearTimeout(timer)
  }, [vrGuideVisible, onDismissVr360Guide])
  const toggleFullscreen = () => {
    const stage = stageRef.current
    if (!stage) return
    if (document.fullscreenElement) void document.exitFullscreen()
    else void stage.requestFullscreen?.()
  }

  // 自定义字幕驱动逻辑：
  // 把所选轨道设为 mode = 'hidden'（让浏览器在后台静默解析时间轴，但不渲染原生带黑底的字幕框），
  // 由下方的 React 自定义层输出 100% 纯透明背景、高清晰文字阴影的字幕。
  useEffect(() => {
    const video = videoRef.current
    const selectedTrack = subs[subtitleIndex]
    if (
      !video ||
      !tracksArmed ||
      subs.length === 0 ||
      subtitleIndex < 0 ||
      !selectedTrack ||
      (selectedTrack.delivery !== 'webvtt' &&
        !(selectedTrack.delivery === 'ass' && assFallbackPath === selectedTrack.path))
    ) {
      setActiveCues([])
      return
    }

    const updateCue = () => {
      const absoluteTime = video.currentTime + (streamOffset ?? 0)
      if (subtitleTimeline?.path === selectedTrack.path) {
        const cues = uniqueSubtitleCues(
          subtitleTimeline.cues.filter(
            (cue) => absoluteTime >= cue.startTime && absoluteTime <= cue.endTime,
          ),
        ).map((cue) => ({ ...cue, text: subtitleChineseConverter(cue.text) }))
        setActiveCues(cues)
        return
      }

      const selectedEl = video.querySelector<HTMLTrackElement>(
        `track[data-subtitle-index="${subtitleIndex}"]`,
      )
      const tt = selectedEl?.track
      if (!tt) {
        setActiveCues([])
        return
      }

      // 优先从浏览器 activeCues 中取当前文本；若浏览器在 hidden 模式下延迟触发 cuechange，
      // 则从 tt.cues 中根据 video.currentTime 实时匹配当前字幕，确保初次加载无感立即可见。
      const cues: SubtitleCue[] = []
      if ((!streamOffset || streamOffset <= 0.05) && tt.activeCues && tt.activeCues.length > 0) {
        for (let i = 0; i < tt.activeCues.length; i++) {
          const cue = tt.activeCues[i] as VTTCue
          if (cue && cue.text) {
            cues.push({ startTime: cue.startTime, endTime: cue.endTime, text: cue.text, settings: {} })
          }
        }
      } else if (tt.cues && tt.cues.length > 0) {
        for (let i = 0; i < tt.cues.length; i++) {
          const cue = tt.cues[i] as VTTCue
          if (
            cue &&
            absoluteTime >= cue.startTime &&
            absoluteTime <= cue.endTime &&
            cue.text
          ) {
            cues.push({ startTime: cue.startTime, endTime: cue.endTime, text: cue.text, settings: {} })
          }
        }
      }
      setActiveCues(
        uniqueSubtitleCues(cues).map((cue) => ({
          ...cue,
          text: subtitleChineseConverter(cue.text),
        })),
      )
    }

    const apply = () => {
      const trackEls = Array.from(video.querySelectorAll('track'))
      if (trackEls.length === 0) return
      trackEls.forEach((el) => {
        const tt = el.track
        if (tt) {
          // 'hidden' 模式：浏览器解析 WebVTT 并触发 cuechange，但隐藏原生黑底 UI
          tt.mode =
            el.dataset.subtitleIndex === String(subtitleIndex) ? 'hidden' : 'disabled'
        }
      })

      const selected = video.querySelector<HTMLTrackElement>(
        `track[data-subtitle-index="${subtitleIndex}"]`,
      )
      if (!selected) return

      const tt = selected.track
      if (tt) {
        tt.removeEventListener('cuechange', updateCue)
        tt.addEventListener('cuechange', updateCue)
      }

      selected.removeEventListener('load', updateCue)
      selected.addEventListener('load', updateCue)

      updateCue()
    }

    apply()
    video.addEventListener('loadedmetadata', apply)
    video.addEventListener('timeupdate', updateCue)
    video.addEventListener('seeking', updateCue)
    video.addEventListener('seeked', updateCue)
    video.addEventListener('playing', updateCue)

    return () => {
      video.removeEventListener('loadedmetadata', apply)
      video.removeEventListener('timeupdate', updateCue)
      video.removeEventListener('seeking', updateCue)
      video.removeEventListener('seeked', updateCue)
      video.removeEventListener('playing', updateCue)
      const selected = video.querySelector<HTMLTrackElement>(
        `track[data-subtitle-index="${subtitleIndex}"]`,
      )
      if (selected) {
        selected.removeEventListener('load', updateCue)
        if (selected.track) {
          selected.track.removeEventListener('cuechange', updateCue)
        }
      }
    }
  }, [
    subtitleIndex,
    subs,
    videoRef,
    media,
    streamOffset,
    subtitleTimeline,
    tracksArmed,
    subtitleChineseConverter,
    assFallbackPath,
  ])

  // 根据视频画面宽高比与舞台宽高比，确定视频在哪个轴向撑满 100%
  const isWiderThanStage =
    videoRatio && stageRect && stageRect.height > 0
      ? videoRatio > stageRect.width / stageRect.height
      : true

  const assTrack = subtitleIndex >= 0 ? subs[subtitleIndex] : undefined
  // VR 模式下画面由 WebGL 画布输出，舞台窗口本身就是「镜头」，
  // 不再按视频宽高比留黑边。
  const wrapperStyle = vr360
    ? {
        width: '100%',
        height: '100%',
      }
    : videoRatio
      ? {
          aspectRatio: `${videoRatio}`,
          width: isWiderThanStage ? '100%' : 'auto',
          height: isWiderThanStage ? 'auto' : '100%',
          maxWidth: '100%',
          maxHeight: '100%',
        }
      : {
          width: '100%',
          height: '100%',
        }

  return (
    <div
      ref={stageRef}
      data-player-stage
      className="relative flex h-full w-full flex-1 items-center justify-center overflow-hidden bg-black"
      onPointerDown={handleStagePointerDown}
      onPointerMove={handleStagePointerMove}
      onMouseLeave={() => setLockHovered(false)}
      onClick={handleSurfaceActivate}
      onDoubleClick={toggleFullscreen}
    >
      {media ? (
        <>
          <div
            className="relative flex items-center justify-center overflow-hidden"
            style={wrapperStyle}
          >
            <video
              ref={videoRef}
              autoPlay
              playsInline
              className="h-full w-full object-contain bg-black"
              onError={onVideoError}
            >
              {tracksArmed &&
                subs.map((track, index) => {
                  const renderAsFallback =
                    track.delivery === 'ass' && assFallbackPath === track.path
                  if (track.delivery !== 'webvtt' && !renderAsFallback) return null
                  return (
                    <track
                      key={track.path}
                      data-subtitle-index={index}
                      kind="subtitles"
                      src={subtitlesAPI.url(media.id, track.path)}
                      srcLang={track.lang}
                      label={track.label || track.lang}
                    />
                  )
                })}
            </video>
            {vr360 ? (
              <Vr360Stage
                key={`${media.id}:vr360`}
                videoRef={videoRef}
                profile={vr360}
                uiVisible={controlsVisible}
                onSurfaceTap={handleSurfaceActivate}
                onReady={() => setVrReadyMediaId(media.id)}
                onError={(message) => onVr360Error?.(message)}
                onProfileChange={onVr360ProfileChange}
                onExitVr={onToggleVr360}
                onUiHoldChange={setVrUiHovered}
              />
            ) : null}
            <DanmakuStage
              key={media.id}
              media={media}
              videoRef={videoRef}
              enabled={danmakuEnabled}
              opacity={danmakuOpacity}
              fontSize={danmakuFontSize}
              area={danmakuArea}
              search={danmakuSearch}
              episodeId={danmakuEpisodeId}
              searchTrigger={danmakuSearchTrigger}
              onLoaded={onDanmakuLoaded}
              onCandidates={onDanmakuCandidates}
              onAlternatives={onDanmakuAlternatives}
            />
            {tracksArmed && media && assTrack && assFallbackPath !== assTrack.path ? (
              <AssSubtitleStage
                key={`${media.id}:${assTrack.path}`}
                videoRef={videoRef}
                source={subtitlesAPI.assUrl(media.id, assTrack.path)}
                timeOffset={streamOffset}
                chineseMode={assTrack.source === 'external' ? subtitleChineseMode : 'original'}
                onError={(error) => {
                  console.warn('libass subtitle rendering failed; falling back to WebVTT', error)
                  setAssFallbackPath(assTrack.path)
                }}
              />
            ) : null}
            {/* SRT/VTT 自定义字幕层：按 cue 位置对齐，并支持用户样式覆盖。 */}
            {activeCues.length > 0 ? (
              <div className="pointer-events-none absolute inset-0 z-10">
                {buildSubtitleRenderGroups(activeCues, subtitlePosition).map((group) => (
                  <div
                    key={group.key}
                    className="absolute flex px-4"
                    style={group.style}
                  >
                    <span
                      className="inline-block whitespace-pre-line font-sans text-white select-none"
                      style={{
                        ...subtitleTextStyle(subtitleStyle),
                        fontSize: 'clamp(16px, 2.1vw, 30px)',
                        lineHeight: 1.35,
                      }}
                    >
                      {group.text}
                    </span>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
          <PlayerControls
            videoRef={videoRef}
            volume={playerVolume}
            onVolumeChange={onPlayerVolumeChange}
            onVolumeCommit={onPlayerVolumeCommit}
            playbackRate={playerPlaybackRate}
            onPlaybackRateChange={onPlayerPlaybackRateChange}
            onPlaybackRateCommit={onPlayerPlaybackRateCommit}
            uiVisible={controlsVisible && !locked}
            onUiVisibleChange={setControlsVisible}
            uiHold={vrUiHovered}
            uiLocked={locked}
            subs={subs}
            subtitleIndex={subtitleIndex}
            onSelectSubtitle={onSelectSubtitle}
            subtitleChineseMode={subtitleChineseMode}
            onSubtitleChineseModeChange={onSubtitleChineseModeChange}
            subtitlePosition={subtitlePosition}
            onSubtitlePositionChange={onSubtitlePositionChange}
            subtitleStyle={subtitleStyle}
            onSubtitleStyleChange={onSubtitleStyleChange}
            danmakuOpen={danmakuOpen}
            danmakuEnabled={danmakuEnabled}
            onToggleDanmakuEnabled={onToggleDanmakuEnabled}
            onOpenDanmaku={onOpenDanmaku}
            hasPrevEpisode={hasPrevEpisode}
            hasNextEpisode={hasNextEpisode}
            onPrevEpisode={onPrevEpisode}
            onNextEpisode={onNextEpisode}
            prevEpisodeTitle={prevEpisodeTitle}
            nextEpisodeTitle={nextEpisodeTitle}
            playlistOpen={playlistOpen}
            hasPlaylist={hasPlaylist}
            hasVersions={hasVersions}
            onTogglePlaylist={onTogglePlaylist}
            knownDuration={knownDuration}
            streamOffset={streamOffset}
            onSeekAbsolute={onSeekAbsolute}
            qualities={qualities}
            qualityLabel={qualityLabel}
            selectedQuality={selectedQuality}
            onSelectQuality={onSelectQuality}
            showQuality={showQuality}
            playbackModeLabel={playbackModeLabel}
            onTogglePlaybackMode={onTogglePlaybackMode}
            vr360={vr360}
            vr360Detected={vr360Detected}
            onToggleVr360={onToggleVr360}
          />
          {danmakuPanel}
          {playlistPanel}
        </>
      ) : loadError ? (
        <p className="max-w-[92%] text-center text-sm text-rose-400">{loadError}</p>
      ) : (
        <p className="text-sand-500">加载中…</p>
      )}
      {vr360 && media && !vrReady ? (
        <div className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center bg-black/60">
          <div className="flex items-center gap-2 rounded-xl border border-white/10 bg-[#0d0e12]/95 px-4 py-3 text-sm text-white shadow-[0_10px_38px_rgba(0,0,0,0.6)] backdrop-blur-xl">
            <Loader2 className="animate-spin text-rose-400" size={18} />
            正在启动 VR 全景渲染…
          </div>
        </div>
      ) : null}
      {/* VR 首次操作说明：按用户只弹一次。刻意不铺全屏遮罩，用户可以一边看说明
          一边拖动画面试操作。 */}
      {vrGuideVisible ? (
        <div className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center px-4">
          <div className="pointer-events-auto w-[min(92vw,400px)] rounded-2xl border border-white/10 bg-[#0d0e12]/95 px-5 py-4 text-white shadow-[0_10px_38px_rgba(0,0,0,0.6)] backdrop-blur-xl">
            <p className="flex items-center gap-2 text-sm font-semibold">
              <Rotate3d size={16} className="text-rose-400" />
              VR 全景播放
            </p>
            <ul className="mt-3 space-y-2 text-xs leading-relaxed text-white/80">
              <li className="flex items-start gap-2">
                <Hand size={14} className="mt-0.5 shrink-0 text-white/45" />
                按住拖动画面：转动视角
              </li>
              <li className="flex items-start gap-2">
                <ZoomIn size={14} className="mt-0.5 shrink-0 text-white/45" />
                滚轮 / 双指捏合：缩放视野
              </li>
              <li className="flex items-start gap-2">
                <MousePointerClick size={14} className="mt-0.5 shrink-0 text-white/45" />
                单击画面：显示或收起操作栏（进度条、清晰度、倍速、设置）
              </li>
              <li className="flex items-start gap-2">
                <Lock size={14} className="mt-0.5 shrink-0 text-white/45" />
                锁默认隐身：电脑把鼠标移到左侧中间、手机点一下画面就会浮现
              </li>
              <li className="flex items-start gap-2">
                <Smartphone size={14} className="mt-0.5 shrink-0 text-white/45" />
                手机上点顶部「陀螺仪」：转动设备就能转动视角
              </li>
            </ul>
            <button
              type="button"
              onClick={onDismissVr360Guide}
              className="mt-4 w-full rounded-xl bg-rose-500 px-4 py-2 text-xs font-medium text-white transition hover:bg-rose-600"
            >
              知道了，不再提示
            </button>
          </div>
        </div>
      ) : null}
      {/* 锁定按钮：贴在画面左侧正中，VR 和普通播放都有，但默认隐身。
          桌面端把鼠标移到左侧中间就会浮现，移开即收；移动端点一下画面唤出，并在
          LOCK_REVEAL_MS 后自动隐身。
          未锁时点一下就把操作栏、进度框和 VR 工具条全部藏起来，同时屏蔽画面轻触
          （不会误触发播放/暂停）；锁住后按钮变淡，提醒「这里是锁」。
          注意：隐身时必须 pointer-events-none——否则这块隐形按钮会吃掉画面单击，
          手机上想点画面唤出锁反而会误触上锁。 */}
      <button
        type="button"
        onClick={(event) => {
          event.stopPropagation()
          toggleLock()
        }}
        // 舞台在 pointerdown 上判断「是不是移动端轻触唤出控制栏」，锁按钮上的
        // 触摸不应该参与那套判断，否则点锁会被当成一次画面轻触。
        onPointerDown={(event) => event.stopPropagation()}
        onDoubleClick={(event) => event.stopPropagation()}
        aria-hidden={!lockVisible}
        tabIndex={lockVisible ? 0 : -1}
        aria-label={locked ? '解锁画面，恢复操作栏' : '锁定画面，隐藏操作栏和进度框'}
        title={locked ? '点一下解锁，恢复操作栏与进度框' : '锁上后画面轻触无效，只留这个锁'}
        className={`absolute left-2.5 top-1/2 z-30 flex h-10 w-10 -translate-y-1/2 items-center justify-center rounded-full border shadow-[0_6px_20px_rgba(0,0,0,0.45)] backdrop-blur transition-opacity duration-200 sm:left-4 ${
          lockVisible ? 'pointer-events-auto opacity-100' : 'pointer-events-none opacity-0'
        } ${
          locked
            ? // 锁住时按钮退到很淡的状态：既提示「这里是锁」，又不打扰观看。
              'border-white/5 bg-black/30 text-white/40 hover:bg-black/60 hover:text-white'
            : 'border-white/10 bg-black/50 text-white/85 hover:bg-black/75 hover:text-white'
        }`}
      >
        {locked ? <Lock size={18} /> : <LockOpen size={18} />}
      </button>
      {/* 解锁提示：操作栏此时已经藏起来了，用户点锁之后需要知道发生了什么。 */}
      {lockHint ? (
        <div className="pointer-events-none absolute left-16 top-1/2 z-30 -translate-y-1/2 rounded-lg border border-white/10 bg-[#0d0e12]/95 px-3 py-1.5 text-xs text-white/85 shadow-[0_10px_38px_rgba(0,0,0,0.6)] backdrop-blur-xl sm:left-20">
          已解锁，点画面可显示操作栏
        </div>
      ) : null}
      {playerError ? (
        <div className="absolute bottom-20 left-1/2 flex w-[min(92vw,720px)] -translate-x-1/2 items-start gap-2.5 rounded-xl border border-rose-500/25 bg-[#0d0e12]/95 px-4 py-3 text-xs leading-relaxed text-white/85 shadow-[0_10px_38px_rgba(0,0,0,0.6)] backdrop-blur-xl">
          <CircleAlert size={15} className="mt-0.5 shrink-0 text-rose-400" />
          <span className="min-w-0 flex-1">{playerError}</span>
        </div>
      ) : null}
      {waiting ? (
        <div className="absolute inset-0 z-40 flex items-center justify-center bg-black/60 px-4 backdrop-blur-sm">
          <div className="max-w-md rounded-2xl border border-white/10 bg-[#0d0e12]/95 px-6 py-5 text-center text-white shadow-[0_10px_38px_rgba(0,0,0,0.6)]">
            <Loader2 className="mx-auto mb-3 animate-spin text-rose-400" size={26} />
            <p className="text-sm font-medium">{waitingMessage || '115 正在转码…'}</p>
            <p className="mt-1 text-xs text-white/55">转码完成后会自动播放</p>
          </div>
        </div>
      ) : null}
    </div>
  )
}
