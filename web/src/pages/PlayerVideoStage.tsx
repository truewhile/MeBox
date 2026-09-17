import { useCallback, useEffect, useRef, useState } from 'react'
import type { CSSProperties, PointerEvent, ReactNode, RefObject } from 'react'
import { Hand, Loader2, MousePointerClick, Rotate3d, Settings2, Smartphone, ZoomIn } from 'lucide-react'

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
import { parseWebVTTCues, type SubtitleCue } from '../utils/subtitleVTT'

type SubtitleRenderGroup = {
  key: string
  text: string
  style: CSSProperties
}

/** VR 模式下必须连续点击这么多次才唤出控制栏：单击太容易在转视角时误触。 */
const VR_TAP_COUNT = 3
/** 相邻两次点击间隔超过该毫秒数就重新开始计数。 */
const VR_TAP_GAP_MS = 900
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
  onToggleDanmaku: () => void
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
  onTogglePlaylist?: () => void
  knownDuration?: number
  streamOffset?: number
  onSeekAbsolute?: (seconds: number) => boolean
  qualities?: PlaybackQuality[]
  selectedQuality?: string
  onSelectQuality?: (quality: PlaybackQuality) => void
  showQuality?: boolean
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
  onToggleDanmaku,
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
  onTogglePlaylist,
  knownDuration,
  streamOffset,
  onSeekAbsolute,
  qualities = [],
  selectedQuality = '',
  onSelectQuality,
  showQuality = false,
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
  // VR 下「连续点击唤出控制栏」已累计的次数（0 表示没在计数）。
  const [vrTapCount, setVrTapCount] = useState(0)
  const vrTapCountRef = useRef(0)
  const vrTapTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
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
  const handleStagePointerDown = (event: PointerEvent<HTMLDivElement>) => {
    // VR 模式下单击不再负责唤出控制栏（改成连续 3 次点击），所以这里不预置开关。
    revealControlsOnlyRef.current = !vrActive && event.pointerType === 'touch' && !controlsVisible
  }

  const resetVrTapCount = useCallback(() => {
    vrTapCountRef.current = 0
    setVrTapCount(0)
    if (vrTapTimerRef.current) {
      clearTimeout(vrTapTimerRef.current)
      vrTapTimerRef.current = null
    }
  }, [])

  // 舞台任意位置的「激活」（轻触/单击）：移动端控制栏隐藏时首次轻触只唤出
  // 控制栏，控制栏已显示时再次轻触才切换播放/暂停。
  // VR 模式下单击不切换播放，也不唤出控制栏：单击太容易误触，必须连续点击
  // VR_TAP_COUNT 次才会显示控制栏；控制栏已显示时单击即收起。
  const handleSurfaceActivate = () => {
    if (revealControlsOnlyRef.current) {
      revealControlsOnlyRef.current = false
      setControlsVisible(true)
      return
    }
    revealControlsOnlyRef.current = false
    if (vrActive) {
      if (controlsVisible) {
        resetVrTapCount()
        setControlsVisible(false)
        return
      }
      const next = vrTapCountRef.current + 1
      vrTapCountRef.current = next
      setVrTapCount(next)
      if (vrTapTimerRef.current) {
        clearTimeout(vrTapTimerRef.current)
        vrTapTimerRef.current = null
      }
      if (next >= VR_TAP_COUNT) {
        resetVrTapCount()
        setControlsVisible(true)
        return
      }
      // 相邻两次点击间隔超过 VR_TAP_GAP_MS 就重新计数，避免把零散点击累加起来。
      vrTapTimerRef.current = setTimeout(() => {
        vrTapTimerRef.current = null
        vrTapCountRef.current = 0
        setVrTapCount(0)
      }, VR_TAP_GAP_MS)
      return
    }
    togglePlay()
  }
  // 进入 VR 全景播放时先亮一次控制栏与 VR 工具条，让用户知道入口在哪；
  // 之后完全由自动隐藏和「连点画面」控制。退出 VR 时要清掉 VR 浮层的悬停状态，
  // 否则控制栏会以为还有浮层被悬停而一直不隐藏。
  useEffect(() => {
    resetVrTapCount()
    if (vrActive) {
      setControlsVisible(true)
      return
    }
    setVrUiHovered(false)
  }, [vrActive, resetVrTapCount])
  useEffect(
    () => () => {
      if (vrTapTimerRef.current) clearTimeout(vrTapTimerRef.current)
    },
    [],
  )

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
            uiVisible={controlsVisible}
            onUiVisibleChange={setControlsVisible}
            uiHold={vrUiHovered}
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
            onToggleDanmaku={onToggleDanmaku}
            hasPrevEpisode={hasPrevEpisode}
            hasNextEpisode={hasNextEpisode}
            onPrevEpisode={onPrevEpisode}
            onNextEpisode={onNextEpisode}
            prevEpisodeTitle={prevEpisodeTitle}
            nextEpisodeTitle={nextEpisodeTitle}
            playlistOpen={playlistOpen}
            hasPlaylist={hasPlaylist}
            onTogglePlaylist={onTogglePlaylist}
            knownDuration={knownDuration}
            streamOffset={streamOffset}
            onSeekAbsolute={onSeekAbsolute}
            qualities={qualities}
            selectedQuality={selectedQuality}
            onSelectQuality={onSelectQuality}
            showQuality={showQuality}
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
          <div className="flex items-center gap-2 rounded-full border border-white/15 bg-black/80 px-5 py-3 text-sm text-white shadow-2xl backdrop-blur">
            <Loader2 className="animate-spin text-rose-400" size={18} />
            正在启动 VR 全景渲染…
          </div>
        </div>
      ) : null}
      {/* VR 首次操作说明：按用户只弹一次。刻意不铺全屏遮罩，用户可以一边看说明
          一边拖动画面试操作。 */}
      {vrGuideVisible ? (
        <div className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center px-4">
          <div className="pointer-events-auto w-[min(92vw,400px)] rounded-2xl border border-white/15 bg-black/85 px-5 py-4 text-white shadow-2xl backdrop-blur">
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
                连续点击画面 {VR_TAP_COUNT} 次：显示控制栏（进度条、音量、画质、倍速、VR 设置）
              </li>
              <li className="flex items-start gap-2">
                <Settings2 size={14} className="mt-0.5 shrink-0 text-white/45" />
                控制栏显示时单击画面即可收起
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
      {/* 连点计数反馈：没有反馈时用户连点两次会觉得没反应。 */}
      {vrActive && !vrGuideVisible && !controlsVisible && vrTapCount > 0 ? (
        <div className="pointer-events-none absolute bottom-24 left-1/2 z-30 -translate-x-1/2 rounded-full border border-white/15 bg-black/75 px-4 py-1.5 text-xs text-white/90 shadow-xl backdrop-blur">
          再连点 {VR_TAP_COUNT - vrTapCount} 次显示控制栏
        </div>
      ) : null}
      {playerError ? (
        <div className="absolute bottom-20 left-1/2 w-[min(92vw,720px)] -translate-x-1/2 rounded-2xl border border-white/15 bg-black/75 px-5 py-4 text-sm text-white shadow-2xl backdrop-blur">
          {playerError}
        </div>
      ) : null}
      {waiting ? (
        <div className="absolute inset-0 z-40 flex items-center justify-center bg-black/60 px-4 backdrop-blur-sm">
          <div className="max-w-md rounded-xl border border-white/15 bg-black/85 px-6 py-5 text-center text-white shadow-2xl">
            <Loader2 className="mx-auto mb-3 animate-spin text-rose-400" size={28} />
            <p className="text-sm font-medium">{waitingMessage || '115 正在转码…'}</p>
            <p className="mt-1 text-xs text-white/60">转码完成后会自动播放</p>
          </div>
        </div>
      ) : null}
    </div>
  )
}
