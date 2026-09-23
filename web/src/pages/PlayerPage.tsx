import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import type Hls from 'hls.js'
import toast from 'react-hot-toast'

import { mediaAPI, libraryAPI } from '../api/library'
import { cloudHlsURL, hlsURL, postPlaybackProgressKeepalive, stopHLSJob, streamURL } from '../api/client'
import {
  danmakuAPI,
  type DanmakuAnime,
  type DanmakuConfig,
  type DanmakuLoadedInfo,
  type DanmakuSettingsPatch,
} from '../api/danmaku'
import { playbackAPI } from '../api/playback'
import { subtitlesAPI, type SubtitleTrack } from '../api/subtitles'
import { systemAPI } from '../api/system'
import { profileAPI } from '../api/profile'
import { useAuthStore } from '../stores/auth'
import type { Media, PlaybackInfo, PlaybackQuality, PlaybackSegment, PlaybackSegmentKind } from '../types'
import { getSeriesKey, seriesTitleFromPath } from '../utils/groupSeries'
import { mediaVersionMatches, mediaVersionsOf } from '../utils/mediaVersion'
import { normalizePlaybackRate } from '../utils/playbackRate'
import { resolveActiveSkip, skippedNoticeText, toSkipSegments, type SkipPrompt } from '../utils/skipSegments'
import { isRemoteEmbyID } from '../utils/remoteEmby'
import {
  normalizeSubtitleChineseMode,
  type SubtitleChineseMode,
} from '../utils/subtitleChinese'
import {
  loadSubtitlePosition,
  loadSubtitleStyle,
  saveSubtitlePosition,
  saveSubtitleStyle,
  type SubtitlePosition,
  type SubtitleStylePreset,
} from '../utils/subtitleDisplay'
import { pickPlayerMode, needsTranscodeForBrowser, isDirectStreamMedia, isStrmMedia, type PlayerMode } from './playerPageModel'
import { classifyDirectPlayError } from './directPlayError'
import { apiErrorMessage } from './StrmManagePage'
import { PlayerTopBar } from './PlayerTopBar'
import { PlayerVideoStage } from './PlayerVideoStage'
import { PlayerMobileTheaterInfo } from '../components/PlayerMobileTheaterInfo'
import { useIsMobileTheater } from '../hooks/useIsMobileTheater'
import { PlayerDanmakuPanel } from '../components/PlayerDanmakuPanel'
import { PlayerPlaylistPanel } from '../components/PlayerPlaylistPanel'
import {
  detectVr360Profile,
  loadVr360Preference,
  sameVr360Profile,
  saveVr360Preference,
  type Vr360Detection,
  type Vr360Profile,
} from '../utils/vr360'

// Fullscreen, dark-themed video page.
//
//   ?mode=hls       force HLS even when direct play would work
//   ?mode=direct    force direct play (default for browser-friendly codecs)
//
// We pick a sensible default based on the source codec: H.264 + AAC in
// MP4 / WebM containers play directly; everything else (HEVC, MKV, AV1,
// AC3 audio, …) gets routed through ffmpeg → HLS. STRM / 云盘直链默认直连，
// 浏览器播不了时再切 HLS。远程 Emby 挂载只能直连。
//
// External subtitles next to the source file are auto-discovered and
// attached as <track> elements.
type PlaybackProgressSession = {
  mediaId: string
  id: string
  startedAtMs: number
  sequence: number
}

// 自动跳过片头后，「已跳过 · 撤销」提示停留的时长。
const SKIP_NOTICE_MS = 6000

function normalizePlayerVolume(value: unknown): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return 1
  return Math.min(1, Math.max(0, parsed))
}

function newPlaybackProgressSession(mediaId: string): PlaybackProgressSession {
  const randomID =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`
  return {
    mediaId,
    id: `${mediaId}:${randomID}`,
    startedAtMs: Date.now(),
    sequence: 0,
  }
}

export function PlayerPage() {
  const { id = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const location = useLocation()

  const ref = useRef<HTMLVideoElement>(null)
  const hlsRef = useRef<Hls | null>(null)
  const lastSentRef = useRef(0)
  const progressSessionRef = useRef<PlaybackProgressSession | null>(null)
  const directRetryRef = useRef(false)
  const retryingDirectRef = useRef(false)
  const fallbackTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const [media, setMedia] = useState<Media | null>(null)
  // VR 全景播放：null = 普通播放。识别结果与用户开关只存本机，
  // 不改动数据库，换设备/换浏览器不会互相影响。
  const [vr360, setVr360] = useState<Vr360Profile | null>(null)
  const [vr360Detected, setVr360Detected] = useState(false)
  const vr360TouchedRef = useRef(false)
  // VR 取帧失败后的自愈：只尝试一次「切到 HLS」，并在切换窗口内忽略重复报错。
  const vr360TextureRetryUsedRef = useRef(false)
  const vr360TextureRetryUntilRef = useRef(0)
  // VR 全景下 STRM/网盘直连必须由服务端同源转发，浏览器才允许 WebGL 读取画面；
  // 普通播放仍然 302 直连，避免把网盘流量压到服务器。放在 ref 里是为了让
  // handleVideoError 里的 src 比对始终拿到最新值。
  const vr360DirectProxy = Boolean(vr360) && isStrmMedia(media)
  const vr360DirectProxyRef = useRef(vr360DirectProxy)
  useEffect(() => {
    vr360DirectProxyRef.current = vr360DirectProxy
  }, [vr360DirectProxy])
  const [mode, setMode] = useState<PlayerMode>('direct')
  const [subs, setSubs] = useState<SubtitleTrack[]>([])
  const [subtitleIndex, setSubtitleIndex] = useState<number>(0)
  const authUser = useAuthStore((state) => state.user)
  const setAuthUser = useAuthStore((state) => state.setUser)
  const [subtitleChineseMode, setSubtitleChineseMode] =
    useState<SubtitleChineseMode>(() =>
      normalizeSubtitleChineseMode(authUser?.subtitle_chinese_mode),
    )
  const [subtitlePosition, setSubtitlePosition] = useState<SubtitlePosition>(loadSubtitlePosition)
  const [subtitleStyle, setSubtitleStyle] = useState<SubtitleStylePreset>(loadSubtitleStyle)
  const [playerVolume, setPlayerVolume] = useState(() =>
    normalizePlayerVolume(authUser?.player_volume),
  )
  const [playerPlaybackRate, setPlayerPlaybackRate] = useState(() =>
    normalizePlaybackRate(authUser?.player_playback_rate),
  )
  const persistedSubtitleChineseModeRef = useRef(subtitleChineseMode)
  // VR 全景播放的首次操作说明是否已经看过：null = 服务端配置还没读回来，
  // 这时不弹说明，避免给老用户闪一下。
  const [vr360GuideSeen, setVr360GuideSeen] = useState<boolean | null>(null)
  const playerVolumeTouchedRef = useRef(false)
  const playerPlaybackRateTouchedRef = useRef(false)
  const playerPlaybackRateSaveSeqRef = useRef(0)
  const subtitlePreferenceTouchedRef = useRef(false)
  const subtitlePreferenceSaveQueueRef = useRef<Promise<void>>(Promise.resolve())
  const [hlsUnavailable, setHlsUnavailable] = useState(false)
  const [playerError, setPlayerError] = useState('')
  // 媒体元数据加载失败（404 / 无权限等）：舞台区直接展示错误而不是永远「加载中」
  const [loadError, setLoadError] = useState('')
  // 「客户端直连解码」模式：宿主机不转码，播放器强制 direct play、隐藏 HLS 切换。
  const [directOnly, setDirectOnly] = useState(false)
  const [directOnlyKnown, setDirectOnlyKnown] = useState(false)
  const [resumePosition, setResumePosition] = useState(0)
  const [initialSeekDone, setInitialSeekDone] = useState(false)
  // HLS session source offset: playlist t=0 maps to this absolute second.
  const [hlsStartSec, setHlsStartSec] = useState(0)
  // 统一播放能力：115 提供 direct → cloud_hls → local_hls，其它源 direct → local_hls。
  const [playbackInfo, setPlaybackInfo] = useState<PlaybackInfo | null>(null)
  const [hlsSource, setHlsSource] = useState<'cloud' | 'local'>('local')
  const [selectedQuality, setSelectedQuality] = useState('')
  const [cloudWaiting, setCloudWaiting] = useState(false)
  const [cloudWaitMessage, setCloudWaitMessage] = useState('')
  const [cloudWaitStartedAt, setCloudWaitStartedAt] = useState(0)
  const cloudPollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cloudRetryRef = useRef(5)
  const pendingSeekRef = useRef<number | null>(null)

  // 弹幕控制：状态来自 /api/danmaku/config 初始值，用户在面板里实时调整。
  const [danmakuOpen, setDanmakuOpen] = useState(false)
  const [danmakuEnabled, setDanmakuEnabled] = useState(true)
  const [danmakuSearch, setDanmakuSearch] = useState<string | null>(null)
  const [danmakuSearchTrigger, setDanmakuSearchTrigger] = useState(0)
  const [danmakuSearching, setDanmakuSearching] = useState(false)
  // 用户从候选列表选定的弹幕库；null = 自动匹配。
  const [danmakuEpisodeId, setDanmakuEpisodeId] = useState<number | string | null>(null)
  // 自动匹配歧义（多番剧命中）时的候选列表。
  const [danmakuCandidates, setDanmakuCandidates] = useState<DanmakuAnime[]>([])
  // 同一集的其它可选来源（弹幕已自动加载，用户可随时切换，无需重新搜索）。
  const [danmakuAlternatives, setDanmakuAlternatives] = useState<DanmakuAnime[]>([])
  // 已加载弹幕的元数据信息（番剧名、单集名、条数、匹配模式等）。
  const [danmakuInfo, setDanmakuInfo] = useState<DanmakuLoadedInfo | null>(null)
  // 用户当前选定的弹幕来源描述（面板中展示）。
  const [danmakuSelectedSource, setDanmakuSelectedSource] = useState('')
  // 弹幕合并偏好：从 /danmaku/config 读取（按用户落库），切换后写回并重新抓取。
  const [danmakuMergeSources, setDanmakuMergeSources] = useState(false)
  const [danmakuMergeSaving, setDanmakuMergeSaving] = useState(false)
  const [danmakuOpacity, setDanmakuOpacity] = useState(1)
  const [danmakuFontSize, setDanmakuFontSize] = useState(24)
  const [danmakuArea, setDanmakuArea] = useState(1)
  const [danmakuSource, setDanmakuSource] = useState('')
  const [danmakuAppID, setDanmakuAppID] = useState('')
  const [danmakuAppKeyConfigured, setDanmakuAppKeyConfigured] = useState(false)
  const [playerSettingsSaving, setPlayerSettingsSaving] = useState(false)

  // 选集 / 播放列表状态
  const [playlistEpisodes, setPlaylistEpisodes] = useState<Media[]>([])
  const [playlistOpen, setPlaylistOpen] = useState(false)
  // 竖屏剧场模式：每次从操作栏点「选集」都 +1，把视频下方的选集区滚回视野。
  const [episodeRevealToken, setEpisodeRevealToken] = useState(0)

  const teardownHls = useCallback(() => {
    if (hlsRef.current) {
      hlsRef.current.destroy()
      hlsRef.current = null
    }
  }, [])

  const backTarget = useCallback(() => {
    const state = location.state as { from?: string } | null
    if (state?.from) return state.from
    const target = media?.id || id
    return target ? `/media/${target}` : '/'
  }, [id, location.state, media])

  const goBack = useCallback(() => {
    navigate(backTarget(), { replace: true })
  }, [backTarget, navigate])

  // 读取宿主机的「直连解码」开关。开启时全程 direct play，不走 HLS。
  useEffect(() => {
    systemAPI
      .info()
      .then((info) => setDirectOnly(Boolean(info.direct_play_only)))
      .catch(() => setDirectOnly(false))
      .finally(() => setDirectOnlyKnown(true))
  }, [])

  // 每次进入播放器都从数据库刷新用户的字幕转换偏好；本地登录缓存只用作首屏初值。
  useEffect(() => {
    let cancelled = false
    profileAPI
      .get()
      .then((user) => {
        if (cancelled || subtitlePreferenceTouchedRef.current) return
        const savedMode = normalizeSubtitleChineseMode(user.subtitle_chinese_mode)
        persistedSubtitleChineseModeRef.current = savedMode
        setSubtitleChineseMode(savedMode)
        setAuthUser(user)
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [setAuthUser])

  const applyDanmakuConnectionConfig = useCallback((cfg: DanmakuConfig) => {
    setDanmakuSource(cfg.source ?? '')
    setDanmakuAppID(cfg.app_id ?? '')
    setDanmakuAppKeyConfigured(Boolean(cfg.app_key_configured))
  }, [])

  const saveDanmakuSettings = useCallback(
    async (patch: DanmakuSettingsPatch, failureMessage = '播放器设置保存失败，请重试') => {
      setPlayerSettingsSaving(true)
      try {
        const cfg = await danmakuAPI.updateSettings(patch)
        applyDanmakuConnectionConfig(cfg)
        return cfg
      } catch (error) {
        toast.error(failureMessage)
        throw error
      } finally {
        setPlayerSettingsSaving(false)
      }
    },
    [applyDanmakuConnectionConfig],
  )

  // 一次读取当前用户的音量、弹幕渲染参数、服务地址和凭据配置状态。
  useEffect(() => {
    let cancelled = false
    danmakuAPI
      .config()
      .then((cfg) => {
        if (cancelled) return
        const volume = normalizePlayerVolume(cfg.volume)
        if (!playerVolumeTouchedRef.current) {
          setPlayerVolume(volume)
        }
        const playbackRate = normalizePlaybackRate(cfg.playback_rate)
        if (!playerPlaybackRateTouchedRef.current) {
          setPlayerPlaybackRate(playbackRate)
        }
        setDanmakuEnabled(cfg.enabled)
        setDanmakuOpacity(Number(cfg.opacity) || 1)
        setDanmakuFontSize(Number(cfg.font_size) || 24)
        setDanmakuArea(Number(cfg.area) || 1)
        setDanmakuMergeSources(Boolean(cfg.merge_sources))
        setVr360GuideSeen(Boolean(cfg.vr360_guide_seen))
        applyDanmakuConnectionConfig(cfg)
        if (!playerVolumeTouchedRef.current) {
          const current = useAuthStore.getState().user
          if (current) setAuthUser({ ...current, player_volume: volume })
        }
        if (!playerPlaybackRateTouchedRef.current) {
          const current = useAuthStore.getState().user
          if (current) setAuthUser({ ...current, player_playback_rate: playbackRate })
        }
      })
      .catch(() => {
        // 配置读取失败时保持本地默认值，不影响播放。
      })
    return () => {
      cancelled = true
    }
  }, [applyDanmakuConnectionConfig, setAuthUser, authUser?.id])

  const changePlayerVolume = useCallback((next: number) => {
    playerVolumeTouchedRef.current = true
    setPlayerVolume(normalizePlayerVolume(next))
  }, [])

  const commitPlayerVolume = useCallback(
    (next: number) => {
      playerVolumeTouchedRef.current = true
      const volume = normalizePlayerVolume(next)
      setPlayerVolume(volume)
      void danmakuAPI
        .updateSettings({ volume })
        .then((cfg) => {
          const saved = normalizePlayerVolume(cfg.volume)
          setPlayerVolume(saved)
          const current = useAuthStore.getState().user
          if (current) setAuthUser({ ...current, player_volume: saved })
        })
        .catch(() => {
          toast.error('音量保存失败，请重试')
        })
    },
    [setAuthUser],
  )

  const changePlayerPlaybackRate = useCallback((next: number) => {
    playerPlaybackRateTouchedRef.current = true
    setPlayerPlaybackRate(normalizePlaybackRate(next))
  }, [])

  const commitPlayerPlaybackRate = useCallback(
    (next: number) => {
      playerPlaybackRateTouchedRef.current = true
      const playbackRate = normalizePlaybackRate(next)
      setPlayerPlaybackRate(playbackRate)
      const saveSeq = ++playerPlaybackRateSaveSeqRef.current
      void danmakuAPI
        .updateSettings({ playback_rate: playbackRate })
        .then((cfg) => {
          if (saveSeq !== playerPlaybackRateSaveSeqRef.current) return
          const saved = normalizePlaybackRate(cfg.playback_rate)
          setPlayerPlaybackRate(saved)
          const current = useAuthStore.getState().user
          if (current) setAuthUser({ ...current, player_playback_rate: saved })
        })
        .catch(() => {
          if (saveSeq !== playerPlaybackRateSaveSeqRef.current) return
          toast.error('倍速保存失败，请重试')
        })
    },
    [setAuthUser],
  )

  const saveDanmakuAdvanced = useCallback(
    async (values: { source: string; appId: string; appKey: string; clearAppKey: boolean }) => {
      const patch: DanmakuSettingsPatch = {
        source: values.source.trim(),
        app_id: values.appId.trim(),
      }
      if (values.clearAppKey) {
        patch.clear_app_key = true
      } else if (values.appKey.trim()) {
        patch.app_key = values.appKey.trim()
      }
      const cfg = await saveDanmakuSettings(patch, '弹幕服务设置保存失败，请重试')
      setDanmakuSource(cfg.source ?? '')
      setDanmakuAppID(cfg.app_id ?? '')
      setDanmakuAppKeyConfigured(Boolean(cfg.app_key_configured))
      // 地址或凭据变化后立即按新配置重新抓取当前对象的弹幕。
      setDanmakuSearching(true)
      setDanmakuSearchTrigger((prev) => prev + 1)
      toast.success('弹幕服务设置已保存')
    },
    [saveDanmakuSettings],
  )

  // 切换合并开关：先落库，成功后再重新抓取，避免与后端读到的偏好不一致。
  const danmakuChangeMergeSources = useCallback(
    (next: boolean) => {
      setDanmakuMergeSaving(true)
      saveDanmakuSettings({ merge_sources: next })
        .then(() => {
          setDanmakuMergeSources(next)
          setDanmakuSearching(true)
          setDanmakuSearchTrigger((prev) => prev + 1)
        })
        .catch(() => undefined)
        .finally(() => setDanmakuMergeSaving(false))
    },
    [saveDanmakuSettings],
  )

  const danmakuChangeEnabled = useCallback(
    (next: boolean) => {
      setDanmakuEnabled(next)
      void saveDanmakuSettings({ enabled: next }).catch(() => undefined)
    },
    [saveDanmakuSettings],
  )

  const commitDanmakuArea = useCallback(
    (next: number) => {
      void saveDanmakuSettings({ area: next }).catch(() => undefined)
    },
    [saveDanmakuSettings],
  )

  const commitDanmakuOpacity = useCallback(
    (next: number) => {
      void saveDanmakuSettings({ opacity: next }).catch(() => undefined)
    },
    [saveDanmakuSettings],
  )

  const commitDanmakuFontSize = useCallback(
    (next: number) => {
      void saveDanmakuSettings({ font_size: next }).catch(() => undefined)
    },
    [saveDanmakuSettings],
  )

  // 用户手动搜索：带关键词重新拉取（search=null 时按视频名）。
  // loading 状态由 DanmakuStage 拉取完成回调（onLoaded）驱动。
  const searchDanmaku = useCallback((kw: string) => {
    setDanmakuSearching(true)
    setDanmakuCandidates([])
    setDanmakuAlternatives([])
    setDanmakuEpisodeId(null)
    setDanmakuInfo(null)
    setDanmakuSearch(kw || null)
    setDanmakuSearchTrigger((prev) => prev + 1)
  }, [])

  const danmakuLoaded = useCallback((info: DanmakuLoadedInfo | null) => {
    setDanmakuSearching(false)
    setDanmakuInfo(info)
  }, [])

  // 同一集的其它来源：弹幕已自动加载好，这里只更新可切换列表。
  const danmakuGotAlternatives = useCallback((alternatives: DanmakuAnime[]) => {
    setDanmakuAlternatives(alternatives)
  }, [])

  // 多番剧命中（disambiguation）：展示候选让用户选择。
  const danmakuGotCandidates = useCallback((candidates: DanmakuAnime[]) => {
    setDanmakuCandidates(candidates)
    setDanmakuSearching(false)
    setDanmakuInfo(null)
    // 候选是静默返回的（此时没有任何弹幕）；自动打开面板提示用户选择来源。
    if (candidates.length > 0) setDanmakuOpen(true)
  }, [])

  // 用户选定某个弹幕库：显式 episodeId 重新拉取。
  const danmakuSelectEpisode = useCallback((episodeId: number, animeTitle: string, episodeTitle: string) => {
    setDanmakuEpisodeId(episodeId)
    setDanmakuCandidates([])
    // 刻意不清空 danmakuAlternatives：切到别的来源后仍要能继续切换回去，
    // 否则用户每次都得重新搜一遍。
    setDanmakuSearching(true)
    // 展示当前所选来源（面板标题处可见）。
    setDanmakuSelectedSource(episodeTitle ? `${animeTitle}・${episodeTitle}` : animeTitle)
    setDanmakuSearchTrigger((prev) => prev + 1)
  }, [])

  // 回到自动匹配（清除用户手动选择）。
  const danmakuResetAuto = useCallback(() => {
    setDanmakuEpisodeId(null)
    setDanmakuCandidates([])
    setDanmakuAlternatives([])
    setDanmakuSearching(true)
    setDanmakuSearch(null)
    setDanmakuSelectedSource('')
    setDanmakuInfo(null)
    setDanmakuSearchTrigger((prev) => prev + 1)
  }, [])

  // 切换视频时重置媒体与弹幕状态，确保新视频自动重新识别并加载弹幕
  useEffect(() => {
    setMedia(null)
    setLoadError('')
    vr360TouchedRef.current = false
    vr360TextureRetryUsedRef.current = false
    vr360TextureRetryUntilRef.current = 0
    setVr360(null)
    setVr360Detected(false)
    setHlsStartSec(0)
    setPlaybackInfo(null)
    setHlsSource('local')
    setSelectedQuality('')
    setCloudWaiting(false)
    setCloudWaitMessage('')
    setCloudWaitStartedAt(0)
    pendingSeekRef.current = null
    if (cloudPollTimerRef.current) {
      clearTimeout(cloudPollTimerRef.current)
      cloudPollTimerRef.current = null
    }
    setDanmakuEpisodeId(null)
    setDanmakuCandidates([])
    setDanmakuAlternatives([])
    setDanmakuSearch(null)
    setDanmakuSelectedSource('')
    setDanmakuInfo(null)
    setDanmakuSearching(true)
    directRetryRef.current = false
    retryingDirectRef.current = false
    if (fallbackTimerRef.current) {
      clearTimeout(fallbackTimerRef.current)
      fallbackTimerRef.current = null
    }
    return () => {
      if (fallbackTimerRef.current) {
        clearTimeout(fallbackTimerRef.current)
        fallbackTimerRef.current = null
      }
    }
  }, [id])

  // 依赖收敛为 mode 参数的字符串值：避免 params 对象引用每次变化都重复拉取元数据
  const modeParam = params.get('mode') as PlayerMode | null

  const setPlaybackMode = useCallback(
    (next: PlayerMode) => {
      setMode(next)
      const nextParams = new URLSearchParams(window.location.search)
      nextParams.set('mode', next)
      setParams(nextParams, { replace: true })
    },
    [setParams],
  )

  const clearFallbackTimer = useCallback(() => {
    if (fallbackTimerRef.current) {
      clearTimeout(fallbackTimerRef.current)
      fallbackTimerRef.current = null
    }
  }, [])

  // Load metadata and pick a default mode.
  useEffect(() => {
    if (!id) return
    let cancelled = false
    mediaAPI
      .get(id)
      .then((m) => {
        if (cancelled) return
        setMedia(m)
        const isDirect = isDirectStreamMedia(m)
        const auto = pickPlayerMode(m)
        // 直连解码模式以及远程 Emby 挂载忽略 ?mode=hls。STRM 默认直连，但允许手动/失败后切 HLS。
        setMode(directOnly || isDirect ? 'direct' : (modeParam ?? auto))
        setPlayerError('')
        setLoadError('')
      })
      .catch((err: unknown) => {
        if (cancelled) return
        // 404 / 无权限等：给出可见错误提示，避免永久「加载中」
        setLoadError(`无法加载该媒体：${apiErrorMessage(err)}`)
      })
    return () => {
      cancelled = true
    }
  }, [id, modeParam, directOnly])

  // VR 全景素材识别：只在用户没手动改过开关时自动进入 VR 模式。
  // 识别完全基于文件名/路径关键词与画幅比例，误判时点一下 VR 按钮即可退出。
  useEffect(() => {
    if (!media || media.id !== id) return
    const detection = detectVr360Profile({
      title: media.title,
      originalName: media.original_name,
      path: media.path,
      relativePath: media.relative_path,
      width: media.width,
      height: media.height,
    })
    setVr360Detected(detection.confident)
    if (vr360TouchedRef.current) return
    const preference = loadVr360Preference()
    if (!preference.autoDetect || !detection.confident) {
      setVr360(null)
      return
    }
    if (detection.profile.projection !== preference.profile.projection) {
      // 投影方式由文件名决定（360 / 180 / 鱼眼），记住它作为下次的默认值。
      saveVr360Preference({ ...preference, profile: detection.profile })
    }
    setVr360(detection.profile)
  }, [id, media])

  // 远端 Emby 挂载的媒体常常没有宽高元数据（width/height 为 0），等播放器拿到
  // 真实画面尺寸后再补一次识别。HLS 转码是等比缩放，所以比例依然可信。
  useEffect(() => {
    if (!media || media.id !== id) return
    if (media.width > 0 && media.height > 0) return
    const video = ref.current
    if (!video) return
    const refine = () => {
      if (vr360TouchedRef.current || video.videoWidth <= 0 || video.videoHeight <= 0) return
      const detection = detectVr360Profile({
        title: media.title,
        originalName: media.original_name,
        path: media.path,
        relativePath: media.relative_path,
        width: video.videoWidth,
        height: video.videoHeight,
      })
      setVr360Detected(detection.confident)
      const preference = loadVr360Preference()
      if (!preference.autoDetect || !detection.confident) return
      setVr360((current) =>
        current && sameVr360Profile(current, detection.profile) ? current : detection.profile,
      )
    }
    video.addEventListener('loadedmetadata', refine)
    refine()
    return () => video.removeEventListener('loadedmetadata', refine)
  }, [id, media])

  const toggleVr360 = useCallback(() => {
    vr360TouchedRef.current = true
    if (vr360) {
      setVr360(null)
      return
    }
    const current = mediaRef.current
    const detection: Vr360Detection | null = current
      ? detectVr360Profile({
          title: current.title,
          originalName: current.original_name,
          path: current.path,
          relativePath: current.relative_path,
          width: current.width,
          height: current.height,
        })
      : null
    const preference = loadVr360Preference()
    const profile = detection?.profile ?? preference.profile
    saveVr360Preference({ ...preference, profile })
    setVr360(profile)
  }, [vr360])

  const changeVr360Profile = useCallback((profile: Vr360Profile) => {
    vr360TouchedRef.current = true
    setVr360(profile)
    saveVr360Preference({ ...loadVr360Preference(), profile })
  }, [])

  // VR 操作说明是「按用户只弹一次」：用户关掉说明时就落库，之后进入 VR 不再显示。
  // 保存失败只影响下次是否再弹一次，不打断播放，所以本地状态不回滚。
  const dismissVr360Guide = useCallback(() => {
    setVr360GuideSeen(true)
    void danmakuAPI.updateSettings({ vr360_guide_seen: true }).catch(() => undefined)
  }, [])

  // Wire up the actual <video> element when we know the mode.
  // Depend on media.id (not the media object): refreshing duration after
  // MANIFEST_PARSED must not remount HLS or it storms EnsureJob / DELETE.
  const mediaId = media?.id
  const playbackProvider = playbackInfo?.provider

  // 加载统一播放能力：115 云端清晰度 + 本地 HLS 清晰度。
  useEffect(() => {
    if (!mediaId) return
    let cancelled = false
    mediaAPI
      .playbackInfo(mediaId)
      .then((info) => {
        if (cancelled) return
        setPlaybackInfo(info)
        cloudRetryRef.current = Math.max(3, info.transcode.retry_after_sec || 5)
        setSelectedQuality((prev) => {
          if (prev && findPlaybackQualityById(info, prev)) return prev
          return info.default_quality || info.local_qualities?.[0]?.id || ''
        })
      })
      .catch(() => {
        if (!cancelled) setPlaybackInfo(null)
      })
    return () => {
      cancelled = true
    }
  }, [mediaId])

  useEffect(() => {
    cloudRetryRef.current = Math.max(3, playbackInfo?.transcode.retry_after_sec || 5)
  }, [playbackInfo])

  // ── 片头/片尾跳过 ──────────────────────────────────────────────────────────
  // 原始片段（服务端单位）与当前生效档案的「自动跳过片头」开关。
  const [rawSkipSegments, setRawSkipSegments] = useState<PlaybackSegment[]>([])
  const [autoSkipIntro, setAutoSkipIntro] = useState(false)
  // 当前落进的跳过提示；不在任何区间时为 null。
  const [activeSkip, setActiveSkip] = useState<SkipPrompt | null>(null)
  // 自动跳过后的撤销提示。
  const [skipNotice, setSkipNotice] = useState<{
    text: string
    startSec: number
    kind: PlaybackSegmentKind
  } | null>(null)
  // 用户已经处理过的区间类型：本次播放内不再重复提示。
  const [dismissedSkipKinds, setDismissedSkipKinds] = useState<PlaybackSegmentKind[]>([])
  // 用户主动跳进过片头/片尾区间（多半是想重看）：本次播放不再自动跳过该类型，
  // 但按钮保留，想跳随时可以点。
  const [autoSuppressedKinds, setAutoSuppressedKinds] = useState<PlaybackSegmentKind[]>([])
  const skipNoticeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // 标记「这次 seeked 是我们自己跳的」，避免把自己的跳转当成用户手动跳转。
  const selfSkipSeekRef = useRef(false)

  // end_ms 为 0 的区间要用媒体总时长补齐，而时长可能晚于片段到达（STRM/HLS 起播
  // 后才回填 duration_sec），所以派生放在这里，时长更新后区间会自动重算。
  const skipSegments = useMemo(
    () => toSkipSegments(rawSkipSegments, media?.duration_sec || 0),
    [rawSkipSegments, media?.duration_sec],
  )

  useEffect(() => {
    if (!mediaId) return
    let cancelled = false
    setRawSkipSegments([])
    setAutoSkipIntro(false)
    setActiveSkip(null)
    setSkipNotice(null)
    setDismissedSkipKinds([])
    setAutoSuppressedKinds([])
    // 片段数据与播放来源无关，播放开始后异步补抓即可，绝不挡在起播路径上。
    playbackAPI
      .segments(mediaId)
      .then((res) => {
        if (cancelled) return
        setAutoSkipIntro(Boolean(res.auto_skip))
        setRawSkipSegments(res.segments ?? [])
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [mediaId])

  useEffect(() => {
    return () => {
      if (skipNoticeTimerRef.current) clearTimeout(skipNoticeTimerRef.current)
    }
  }, [])

  const switchToLocalHLS = useCallback(
    (position = 0) => {
      setHlsSource('local')
      setCloudWaiting(false)
      setSelectedQuality((prev) => {
        if (playbackInfo?.local_qualities?.some((quality) => quality.id === prev)) return prev
        return defaultLocalQualityId(playbackInfo)
      })
      if (position > 2) setHlsStartSec(position)
      setPlaybackMode('hls')
    },
    [playbackInfo, setPlaybackMode],
  )

  const startCloudTranscode = useCallback(
    async (definition: number) => {
      if (!mediaId) return
      try {
        const info = await mediaAPI.startCloudTranscode(mediaId, definition)
        setPlaybackInfo(info)
        if (info.transcode.state === 'ready') {
          setCloudWaiting(false)
          setCloudWaitMessage('')
          setHlsSource('cloud')
          setPlaybackMode('hls')
          return
        }
        setCloudWaiting(true)
        setCloudWaitMessage(info.transcode.message || '115 正在转码…')
        setCloudWaitStartedAt(Date.now())
      } catch {
        switchToLocalHLS()
        toast.error('115 云端转码触发失败，已切换本地转码')
      }
    },
    [mediaId, setPlaybackMode, switchToLocalHLS],
  )

  // 切到 HLS 播放（优先 115 云端，其次本地转码）。toggleMode 与「VR 需要同源帧」
  // 的自愈逻辑共用，避免两处各写一遍云端/本地选择。
  const enterHlsPlayback = useCallback(
    (startSec: number) => {
      setHlsStartSec(startSec)
      const preferred =
        playbackInfo?.provider === 'cloud115'
          ? findPlaybackQualityById(playbackInfo, selectedQuality) ??
            findPlaybackQualityById(playbackInfo, playbackInfo.default_quality)
          : undefined
      if (preferred?.source === 'cloud') {
        setHlsSource('cloud')
        if (preferred.available) {
          setCloudWaiting(false)
        } else {
          setCloudWaiting(true)
          setCloudWaitMessage(preferred.note || `正在等待 115 转码 ${preferred.label}…`)
          void startCloudTranscode(Number(preferred.id) || 4)
        }
      } else {
        switchToLocalHLS(startSec)
      }
      setPlaybackMode('hls')
    },
    [playbackInfo, selectedQuality, setPlaybackMode, startCloudTranscode, switchToLocalHLS],
  )

  // 云端转码等待：按后端建议间隔轮询，完成后自动切到云 HLS。
  useEffect(() => {
    if (!cloudWaiting || !mediaId) return
    let cancelled = false
    const poll = async () => {
      if (cancelled) return
      if (cloudWaitStartedAt > 0 && Date.now() - cloudWaitStartedAt > 10 * 60 * 1000) {
        switchToLocalHLS()
        toast.error('115 云端转码等待超时，已切换本地转码')
        return
      }
      try {
        const definition = Number(selectedQuality) || undefined
        const info = await mediaAPI.playbackInfo(mediaId, definition)
        if (cancelled) return
        setPlaybackInfo(info)
        cloudRetryRef.current = Math.max(3, info.transcode.retry_after_sec || 5)
        if (info.transcode.state === 'ready') {
          setCloudWaiting(false)
          setCloudWaitMessage('')
          setHlsSource('cloud')
          setPlaybackMode('hls')
          return
        }
        if (info.transcode.state === 'unavailable') {
          switchToLocalHLS()
          toast.error(info.transcode.message || '115 云端转码不可用，已切换本地转码')
          return
        }
        setCloudWaitMessage(info.transcode.message || '115 正在转码…')
        cloudPollTimerRef.current = setTimeout(poll, cloudRetryRef.current * 1000)
      } catch {
        cloudPollTimerRef.current = setTimeout(poll, 5000)
      }
    }
    cloudPollTimerRef.current = setTimeout(poll, cloudRetryRef.current * 1000)
    return () => {
      cancelled = true
      if (cloudPollTimerRef.current) {
        clearTimeout(cloudPollTimerRef.current)
        cloudPollTimerRef.current = null
      }
    }
  }, [cloudWaiting, cloudWaitStartedAt, mediaId, selectedQuality, setPlaybackMode, switchToLocalHLS])

  // 直连播放只发现外挂字幕；只有 HLS 模式需要探测可烧录的内嵌字幕。
  useEffect(() => {
    if (!mediaId) return
    let cancelled = false
    subtitlesAPI
      .list(mediaId, mode === 'hls')
      .then((tracks) => {
        if (cancelled) return
        const list = tracks ?? []
        setSubs(list)
        // 服务端始终把外挂字幕排在内嵌字幕前面，因此第一条就是默认优先轨。
        setSubtitleIndex(list.length > 0 ? 0 : -1)
      })
      .catch(() => {
        if (cancelled) return
        setSubs([])
        setSubtitleIndex(-1)
      })
    return () => {
      cancelled = true
    }
  }, [mediaId, mode])

  const selectedSubtitle = subtitleIndex >= 0 ? subs[subtitleIndex] : undefined
  const burnedSubtitleStream =
    selectedSubtitle?.delivery === 'burn' ? selectedSubtitle.stream_index : undefined
  // 直连不使用烧录字幕参数。字幕列表通常比媒体信息晚返回，若把该参数直接
  // 作为播放 effect 的依赖，会在 STRM 的 302 直链仍在建立时重复设置 src，
  // Chromium 会把被中断的首次加载报告成播放错误并误触发 HLS 回退。
  const activeBurnedSubtitleStream = mode === 'hls' ? burnedSubtitleStream : undefined
  const mediaRef = useRef(media)
  useEffect(() => {
    mediaRef.current = media
  }, [media])

  useEffect(() => {
    if (!mediaId || !ref.current) return
    const currentMedia = mediaRef.current
    if (!currentMedia) return
    let cancelled = false
    teardownHls()

    const video = ref.current
    const durationSec = currentMedia.duration_sec || 0
    if (mode === 'hls') {
      if (hlsSource === 'cloud' && cloudWaiting) {
        teardownHls()
        return
      }
      const url =
        hlsSource === 'cloud'
          ? cloudHlsURL(mediaId, selectedQuality)
          : hlsURL(mediaId, hlsStartSec, activeBurnedSubtitleStream, selectedQuality)
      void import('hls.js').then(({ default: HlsCtor }) => {
        if (cancelled || !ref.current) return
        if (HlsCtor.isSupported()) {
          const hls = new HlsCtor({
            enableWorker: true,
            lowLatencyMode: false,
            // The server waits up to 45s for the first segment. Slow two-core
            // hosts and remote STRM sources regularly need more than hls.js's
            // 10s default, which otherwise aborts a healthy transcode.
            manifestLoadingTimeOut: 60_000,
            manifestLoadingMaxRetry: 1,
          })
          try {
            video.currentTime = 0
          } catch {
            // ignore
          }
          hls.loadSource(url)
          hls.attachMedia(video)
          hls.on(HlsCtor.Events.MANIFEST_PARSED, () => {
            if (pendingSeekRef.current !== null) {
              const target = pendingSeekRef.current
              pendingSeekRef.current = null
              try {
                video.currentTime = target
              } catch {
                // ignore
              }
            }
            void video.play().catch(() => undefined)
            // .strm 入库时常缺 duration；转码启动时会补探测，这里刷新一次给进度条总时长。
            if (durationSec > 0) return
            mediaAPI
              .get(mediaId)
              .then((fresh) => {
                if (cancelled || fresh.id !== mediaId) return
                if ((fresh.duration_sec || 0) > 0) setMedia(fresh)
              })
              .catch(() => undefined)
          })
          hls.on(HlsCtor.Events.ERROR, (_, data) => {
            if (data.fatal) {
              if (hlsSource === 'cloud') {
                const position = ref.current?.currentTime || 0
                setHlsUnavailable(false)
                switchToLocalHLS(position)
                toast.error('115 云端播放失败，切换本地转码')
                return
              }
              setHlsUnavailable(true)
              if (playbackProvider === 'cloud115') {
                setPlayerError('115 云端和本地转码均不可用，请检查账号授权或稍后重试。')
                toast.error('115 云端和本地转码均不可用')
                return
              }
              setPlayerError('HLS 转码不可用，正在尝试直接播放原始文件。若出现有画面无声音，通常是 MKV/AC3/EAC3 音轨需要配置本机 ffmpeg 转码为 AAC。')
              toast.error('HLS 转码失败，尝试切换到直接播放')
              setPlaybackMode('direct')
            }
          })
          if (cancelled) {
            hls.destroy()
            return
          }
          hlsRef.current = hls
        } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
          try {
            video.currentTime = 0
          } catch {
            // ignore
          }
          video.src = url
          void video.play().catch(() => undefined)
        } else {
          setHlsUnavailable(true)
          setPlayerError('当前浏览器不支持 HLS，正在尝试直接播放。')
          toast.error('当前浏览器不支持 HLS，降级到直接播放')
          setPlaybackMode('direct')
        }
      }).catch(() => {
        if (cancelled) return
        setHlsUnavailable(true)
        setPlayerError('HLS 播放组件加载失败，正在尝试直接播放。')
        setPlaybackMode('direct')
      })
    } else {
      // VR 全景需要浏览器能读帧：STRM/网盘直链会被 302 到跨域 CDN，此时改为
      // 服务端同源转发（原画，不转码）；普通播放仍走 302 直连，省服务器流量。
      const url = streamURL(mediaId, { proxy: vr360DirectProxyRef.current })
      const absoluteURL = new URL(url, window.location.href).href
      // 其它异步播放器状态更新不应重启同一个直连请求；STRM 的重定向/换链
      // 比本地文件慢，重启请求可能产生一个短暂但会触发 onError 的中断。
      if (video.src !== absoluteURL) {
        directRetryRef.current = false
        clearFallbackTimer()
        // 进出 VR 会切换播放源地址，这里保住当前播放位置。
        const resumeAt = video.currentTime > 2 ? video.currentTime : 0
        if (resumeAt > 0) {
          video.addEventListener(
            'loadedmetadata',
            () => {
              try {
                video.currentTime = resumeAt
              } catch {
                // ignore
              }
            },
            { once: true },
          )
        }
        video.src = url
        void video.play().catch(() => undefined)
      }
      if (hlsUnavailable && needsTranscodeForBrowser(currentMedia)) {
        setPlayerError('当前正在直连播放原始文件；此封装或音轨浏览器兼容性有限，可能只有画面没有声音。请配置本机 ffmpeg 后切回 HLS 转码播放。')
      }
    }
    const onPlaying = () => clearFallbackTimer()
    video.addEventListener('playing', onPlaying)
    return () => {
      cancelled = true
      video.removeEventListener('playing', onPlaying)
      teardownHls()
    }
  }, [
    activeBurnedSubtitleStream,
    clearFallbackTimer,
    cloudWaiting,
    hlsSource,
    hlsUnavailable,
    hlsStartSec,
    mediaId,
    mode,
    playbackProvider,
    selectedQuality,
    setPlaybackMode,
    switchToLocalHLS,
    teardownHls,
    vr360DirectProxy,
  ])

  // Stop host ffmpeg when leaving this HLS player. The keepalive request also
  // survives route navigation while the component is being torn down.
  useEffect(() => {
    if (!mediaId || mode !== 'hls' || hlsSource !== 'local') return
    return () => {
      stopHLSJob(mediaId)
    }
  }, [hlsSource, mediaId, mode])

  // React cleanup is not guaranteed when a tab/window closes. pagehide fires
  // while the document can still dispatch a keepalive request.
  useEffect(() => {
    if (!mediaId || mode !== 'hls' || hlsSource !== 'local') return
    const stopOnPageExit = () => stopHLSJob(mediaId)
    window.addEventListener('pagehide', stopOnPageExit)
    return () => window.removeEventListener('pagehide', stopOnPageExit)
  }, [hlsSource, mediaId, mode])

  // 自动拉取已有的播放进度并恢复播放位置
  useEffect(() => {
    if (!id) return
    setResumePosition(0)
    setInitialSeekDone(false)
    playbackAPI
      .getResume(id)
      .then((progress) => {
        if (progress.position_ms > 2000 && !progress.completed) {
          setResumePosition(progress.position_ms / 1000)
        }
      })
      .catch(() => undefined)
  }, [id])

  useEffect(() => {
    if (!resumePosition || initialSeekDone) return
    if (mode === 'hls') {
      // Restart transcode near the resume point instead of seeking a short partial playlist.
      if (Math.abs(hlsStartSec - resumePosition) > 2) {
        setHlsStartSec(resumePosition)
      }
      setInitialSeekDone(true)
      const m = Math.floor(resumePosition / 60)
      const s = Math.floor(resumePosition % 60)
      const timeStr = `${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`
      toast.success(`已恢复上次播放进度至 ${timeStr}`, { duration: 2500 })
      return
    }
    const video = ref.current
    if (!video) return
    let onSeeked: (() => void) | undefined
    const applyResume = () => {
      if (resumePosition > 0 && Math.abs(video.currentTime - resumePosition) > 2) {
        onSeeked = () => {
          setInitialSeekDone(true)
          const m = Math.floor(resumePosition / 60)
          const s = Math.floor(resumePosition % 60)
          const timeStr = `${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`
          toast.success(`已恢复上次播放进度至 ${timeStr}`, { duration: 2500 })
        }
        video.addEventListener('seeked', onSeeked, { once: true })
        video.currentTime = resumePosition
        return
      }
      setInitialSeekDone(true)
    }
    if (video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
      applyResume()
    } else {
      video.addEventListener('canplay', applyResume, { once: true })
    }
    return () => {
      video.removeEventListener('canplay', applyResume)
      if (onSeeked) video.removeEventListener('seeked', onSeeked)
    }
  }, [resumePosition, initialSeekDone, mode, hlsStartSec])

  // 使用 ref 实时同步进度计算所需的状态，避免每次 hlsStartSec 改变都触发 cleanup 并误上报旧进度
  const hlsStartSecRef = useRef(hlsStartSec)
  const modeRef = useRef(mode)
  useEffect(() => {
    hlsStartSecRef.current = hlsStartSec
    modeRef.current = mode
  }, [hlsStartSec, mode])

  // Persist resume position every 10 seconds while playing, and immediately upon
  // pause/page hide/unmount. Bind after mediaId is available because
  // PlayerVideoStage does not render the <video> element until media is loaded.
  useEffect(() => {
    if (!id || !mediaId || !ref.current) return
    const video = ref.current
    if (progressSessionRef.current?.mediaId !== mediaId) {
      progressSessionRef.current = newPlaybackProgressSession(mediaId)
      lastSentRef.current = 0
    }

    const absolutePositionMs = () => {
      const currentStartSec = modeRef.current === 'hls' ? hlsStartSecRef.current : 0
      const absolute = currentStartSec + (video.currentTime || 0)
      return Number.isFinite(absolute) ? Math.max(0, Math.floor(absolute * 1000)) : 0
    }
    const absoluteDurationMs = () => {
      const currentStartSec = modeRef.current === 'hls' ? hlsStartSecRef.current : 0
      const mediaDur = mediaRef.current?.duration_sec || 0
      const streamDur = Number.isFinite(video.duration) ? video.duration : 0
      const totalSec = Math.max(mediaDur, currentStartSec + streamDur)
      return Number.isFinite(totalSec) && totalSec > 0 ? Math.floor(totalSec * 1000) : 0
    }
    const send = (keepalive: boolean) => {
      const currentMedia = mediaRef.current
      if (!currentMedia || currentMedia.id !== mediaId) return
      const now = Date.now()
      if (!keepalive && now - lastSentRef.current < 10_000) return
      lastSentRef.current = now
      const positionMs = absolutePositionMs()
      if (positionMs <= 0) return
      const durationMs = absoluteDurationMs()
      const session = progressSessionRef.current
      if (!session || session.mediaId !== mediaId) return
      session.sequence += 1
      const payload = {
        media_id: currentMedia.id,
        position_ms: positionMs,
        duration_ms: durationMs,
        session_id: session.id,
        session_started_at_ms: session.startedAtMs,
        sequence: session.sequence,
      }
      if (keepalive) {
        postPlaybackProgressKeepalive(payload)
        return
      }
      playbackAPI.recordProgress(payload).catch(() => undefined)
    }
    const onTimeUpdate = () => send(false)
    const onPause = () => send(true)
    const onPageHide = () => send(true)
    const onVisibilityChange = () => {
      if (document.visibilityState === 'hidden') send(true)
    }
    video.addEventListener('timeupdate', onTimeUpdate)
    video.addEventListener('pause', onPause)
    window.addEventListener('pagehide', onPageHide)
    document.addEventListener('visibilitychange', onVisibilityChange)
    return () => {
      video.removeEventListener('timeupdate', onTimeUpdate)
      video.removeEventListener('pause', onPause)
      window.removeEventListener('pagehide', onPageHide)
      document.removeEventListener('visibilitychange', onVisibilityChange)
      send(true)
    }
  }, [id, mediaId])

  // 加载剧集/播放列表
  useEffect(() => {
    if (!id) return
    let canceled = false
    mediaAPI
      .getEpisodes(id)
      .then((res) => {
        if (canceled) return
        setPlaylistEpisodes(res.items ?? [])
      })
      .catch(() => {
        if (canceled) return
        if (media && (media.display_library_id || media.library_id)) {
          const libId = media.display_library_id || media.library_id
          const seriesKey = getSeriesKey(media)
          if (seriesKey) {
            libraryAPI
              .listSeriesEpisodes(libId, seriesKey)
              .then((res) => {
                if (!canceled) setPlaylistEpisodes(res.items ?? [])
              })
              .catch(() => {
                if (!canceled) setPlaylistEpisodes([])
              })
            return
          }
        }
        setPlaylistEpisodes([])
      })
    return () => {
      canceled = true
    }
  }, [id, media])

  // 选集列表按版本组折叠，行的 id 是组内最优版本；用户切到同一条目的其它
  // 版本后 media.id 与行 id 不再相等，这里必须按版本组比对，否则上一集/下一集
  // 与自动连播都会失效。
  const currentEpisodeIndex = useMemo(() => {
    if (!media || playlistEpisodes.length === 0) return -1
    return playlistEpisodes.findIndex((e) => mediaVersionMatches(e, media.id))
  }, [media, playlistEpisodes])

  const prevEpisode = useMemo(() => {
    if (currentEpisodeIndex > 0) {
      return playlistEpisodes[currentEpisodeIndex - 1]
    }
    return null
  }, [currentEpisodeIndex, playlistEpisodes])

  const nextEpisode = useMemo(() => {
    if (currentEpisodeIndex >= 0 && currentEpisodeIndex < playlistEpisodes.length - 1) {
      return playlistEpisodes[currentEpisodeIndex + 1]
    }
    return null
  }, [currentEpisodeIndex, playlistEpisodes])

  const prevEpisodeTitle = useMemo(() => {
    return prevEpisode ? formatEpisodeDisplay(prevEpisode, playlistEpisodes) : ''
  }, [prevEpisode, playlistEpisodes])

  const nextEpisodeTitle = useMemo(() => {
    return nextEpisode ? formatEpisodeDisplay(nextEpisode, playlistEpisodes) : ''
  }, [nextEpisode, playlistEpisodes])

  // ── 片头/片尾跳过：位置判定 ────────────────────────────────────────────────
  // 只记录「提示是否变化」，避免 timeupdate（约每秒 4 次）每次都写 state 重渲染。
  const activeSkipKeyRef = useRef('')

  // 监听播放位置，判断当前是否落在某个可跳过区间里。
  useEffect(() => {
    const video = ref.current
    if (!video || skipSegments.length === 0) return
    const sync = () => {
      // HLS 转码时 <video>.currentTime 是相对转码起点的，必须把起点加回来。这里与
      // 进度上报用的算法保持一致，否则转码场景下整段判断都会错位。
      const offset = modeRef.current === 'hls' ? hlsStartSecRef.current : 0
      const absolute = offset + (video.currentTime || 0)
      const next = resolveActiveSkip(absolute, skipSegments, {
        hasNextEpisode: Boolean(nextEpisode),
        excludedKinds: dismissedSkipKinds,
      })
      const key = next ? `${next.kind}:${next.startSec}` : ''
      if (key === activeSkipKeyRef.current) return
      activeSkipKeyRef.current = key
      setActiveSkip(next)
    }
    sync()
    video.addEventListener('timeupdate', sync)
    return () => video.removeEventListener('timeupdate', sync)
  }, [skipSegments, nextEpisode, dismissedSkipKinds, mediaId])

  // 用户主动跳进片头/回顾区间（多半是想重看）：本次播放不再自动跳过该类型，
  // 但按钮保留。只关心会被自动跳过的两种类型。
  useEffect(() => {
    const video = ref.current
    if (!video || skipSegments.length === 0) return
    const onSeeked = () => {
      if (selfSkipSeekRef.current) {
        selfSkipSeekRef.current = false
        return
      }
      const offset = modeRef.current === 'hls' ? hlsStartSecRef.current : 0
      const absolute = offset + (video.currentTime || 0)
      const hit = skipSegments.find(
        (segment) =>
          (segment.kind === 'intro' || segment.kind === 'recap') &&
          absolute >= segment.startSec &&
          absolute < segment.endSec,
      )
      if (!hit) return
      setAutoSuppressedKinds((prev) => (prev.includes(hit.kind) ? prev : [...prev, hit.kind]))
    }
    video.addEventListener('seeked', onSeeked)
    return () => video.removeEventListener('seeked', onSeeked)
  }, [skipSegments, mediaId])

  const playEpisode = useCallback(
    (target: Media) => {
      navigate(
        {
          pathname: `/play/${target.id}`,
          search: location.search,
        },
        { state: location.state },
      )
    },
    [navigate, location.search, location.state],
  )

  // URL 已切换但新媒体尚未返回时，不渲染上一条媒体遗留的版本信息。
  const currentVersions = useMemo(
    () => (media?.id === id ? mediaVersionsOf(media) : []),
    [id, media],
  )
  const switchVersion = useCallback(
    (version: Media) => {
      if (!version?.id || version.id === media?.id) return
      playEpisode(version)
    },
    [media?.id, playEpisode],
  )

  const handlePrevEpisode = useCallback(() => {
    if (prevEpisode) {
      playEpisode(prevEpisode)
    }
  }, [prevEpisode, playEpisode])

  const handleNextEpisode = useCallback(() => {
    if (nextEpisode) {
      playEpisode(nextEpisode)
    }
  }, [nextEpisode, playEpisode])

  const togglePlaylistOpen = useCallback(() => {
    setPlaylistOpen((prev) => {
      const next = !prev
      if (next) setDanmakuOpen(false)
      return next
    })
    setEpisodeRevealToken((token) => token + 1)
  }, [])

  // 操作栏 / 设置面板里的「选集」入口。竖屏剧场模式下选集区就在视频下方，
  // 这里只负责展开并滚过去，不把已经展开的列表收起来（收起由选集区自己的
  // 「收起」按钮负责），这样播放画面永远不会被浮层挡住。
  const openPlaylistEntry = useCallback(() => {
    setPlaylistOpen(true)
    setDanmakuOpen(false)
    setEpisodeRevealToken((token) => token + 1)
  }, [])

  const toggleDanmakuOpen = useCallback(() => {
    setDanmakuOpen((prev) => {
      const next = !prev
      if (next) setPlaylistOpen(false)
      return next
    })
  }, [])

  // 视频播放结束时自动播放下一集
  useEffect(() => {
    if (!ref.current || !nextEpisode) return
    const video = ref.current
    const onEnded = () => {
      toast.success(`正在播放下一集：${nextEpisodeTitle || '下一集'}`)
      playEpisode(nextEpisode)
    }
    video.addEventListener('ended', onEnded)
    return () => {
      video.removeEventListener('ended', onEnded)
    }
  }, [nextEpisode, nextEpisodeTitle, playEpisode])

  // ESC = back 或关闭浮层，[ / ] 或 Shift+P / Shift+N 切换上一集/下一集。
  // 左右方向键的回退/快进在播放器控制栏里处理。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      if (
        target &&
        (target.tagName === 'INPUT' ||
          target.tagName === 'TEXTAREA' ||
          target.isContentEditable)
      ) {
        return
      }

      if (e.key === 'Escape') {
        if (playlistOpen) {
          setPlaylistOpen(false)
          return
        }
        if (danmakuOpen) {
          setDanmakuOpen(false)
          return
        }
        goBack()
      } else if (e.key === '[' || (e.shiftKey && e.key.toLowerCase() === 'p')) {
        if (prevEpisode) {
          e.preventDefault()
          handlePrevEpisode()
        }
      } else if (e.key === ']' || (e.shiftKey && e.key.toLowerCase() === 'n')) {
        if (nextEpisode) {
          e.preventDefault()
          handleNextEpisode()
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [goBack, prevEpisode, nextEpisode, handlePrevEpisode, handleNextEpisode, playlistOpen, danmakuOpen])

  const isDirectStream = isDirectStreamMedia(media)
  const qualityOptions =
    playbackInfo?.provider === 'cloud115'
      ? [...(playbackInfo.cloud_qualities ?? []), ...(playbackInfo.local_qualities ?? [])]
      : (playbackInfo?.local_qualities ?? [])
  // 直连解码模式下宿主机不转码，档位选择没有意义（远程 Emby 挂载同理）：直接隐藏。
  const showQuality = !directOnly && qualityOptions.length > 0
  // 清晰度按钮上显示的文字。直接播放时按「原画」呈现，和档位列表里的原画项一致，
  // 避免出现「明明是原文件却显示 1080P」这种误导。
  const originalQualityLabel =
    qualityOptions.find((quality) => quality.source === 'original')?.label || '原画'
  const selectedQualityLabel = findPlaybackQualityById(playbackInfo, selectedQuality)?.label || ''
  const qualityLabel =
    mode === 'direct' ? originalQualityLabel : selectedQualityLabel || '清晰度'
  // 播放方式只描述状态，切换入口在控制栏的「设置」面板里（见 onTogglePlaybackMode）。
  const playbackModeLabel = isDirectStream
    ? isRemoteEmbyID(media?.id)
      ? 'Emby 直连播放'
      : '直连播放'
    : directOnly
      ? '客户端直连解码'
      : mode === 'hls'
        ? 'HLS 转码'
        : '直接播放'
  const canTogglePlaybackMode = !isDirectStream && !directOnly

  // 顶栏标题下的次要信息：集数进度与版本数量，让用户一眼知道「在看什么、在哪」。
  const topBarTitle = media?.title?.trim() || ''
  const topBarSubtitle = useMemo(() => {
    if (!media) return ''
    const parts: string[] = []
    if (prevEpisode || nextEpisode) {
      const total = playlistEpisodes.length
      const index = currentEpisodeIndex + 1
      parts.push(total > 0 ? `第 ${index} / ${total} 集` : `第 ${index} 集`)
    }
    if (currentVersions.length > 1) parts.push(`${currentVersions.length} 个版本`)
    return parts.join(' · ')
  }, [
    media,
    currentEpisodeIndex,
    currentVersions.length,
    nextEpisode,
    playlistEpisodes.length,
    prevEpisode,
  ])

  // 没有外挂字幕且第一条内嵌字幕是图片时，默认轨需要通过 HLS 烧录。
  useEffect(() => {
    if (
      !directOnlyKnown ||
      directOnly ||
      isDirectStream ||
      selectedSubtitle?.delivery !== 'burn' ||
      mode === 'hls'
    ) {
      return
    }
    setHlsStartSec(ref.current?.currentTime || 0)
    setPlaybackMode('hls')
  }, [
    directOnly,
    directOnlyKnown,
    isDirectStream,
    mode,
    selectedSubtitle?.delivery,
    setPlaybackMode,
  ])

  const toggleMode = useCallback(() => {
    if (isDirectStream) {
      toast('该媒体为直连播放，无需且不支持转码')
      return
    }
    const next = mode === 'hls' ? 'direct' : 'hls'
    if (next === 'hls') {
      enterHlsPlayback(0)
    } else {
      setCloudWaiting(false)
    }
    setPlaybackMode(next)
  }, [
    enterHlsPlayback,
    isDirectStream,
    mode,
    setPlaybackMode,
  ])

  // VR 渲染取不到帧时：网盘/STRM 的直连会 302 跳到 CDN，视频就变成跨域资源，
  // 浏览器禁止 WebGL 读取（texImage2D 抛 SecurityError）。此时自动切到同源的
  // HLS（云端优先）再继续 VR，只有确实做不到时才如实报错并退出 VR。
  const handleVr360Error = useCallback(
    (message: string) => {
      const now = Date.now()
      // 切换 HLS 的过程中旧的直连帧还会继续上报失败，先忽略这一窗口内的重复错误。
      if (now < vr360TextureRetryUntilRef.current) return
      const canRetryWithHls =
        !vr360TextureRetryUsedRef.current &&
        modeRef.current === 'direct' &&
        !directOnly &&
        !isRemoteEmbyID(mediaRef.current?.id)
      if (canRetryWithHls) {
        vr360TextureRetryUsedRef.current = true
        vr360TextureRetryUntilRef.current = now + 15_000
        setPlayerError('')
        toast('直连源跨域，浏览器无法取帧；正在切到 HLS 转码后继续 VR 全景')
        enterHlsPlayback(ref.current?.currentTime || 0)
        return
      }
      vr360TouchedRef.current = true
      setVr360(null)
      setPlayerError(
        `${message}。若当前是网盘/STRM 直连（会 302 跳转到 CDN），浏览器不允许 WebGL 读取跨域画面，请改用 HLS 播放后再开启 VR。`,
      )
      toast.error(message)
    },
    [directOnly, enterHlsPlayback],
  )

  const handleSeekAbsolute = useCallback(
    (absoluteSec: number) => {
      if (mode !== 'hls') return false
      const video = ref.current
      if (!video) return false
      const target = Math.max(0, absoluteSec)
      const local = target - hlsStartSec
      // Live/EVENT HLS during transcoding often reports duration=Infinity.
      // Never treat that as "already buffered" or currentTime seeks reset to 0.
      const finiteDuration = Number.isFinite(video.duration) ? video.duration : 0
      let seekableEnd = 0
      if (video.seekable && video.seekable.length > 0) {
        try {
          seekableEnd = video.seekable.end(video.seekable.length - 1)
        } catch {
          seekableEnd = 0
        }
      }
      if (!Number.isFinite(seekableEnd)) seekableEnd = 0
      const windowEnd = Math.max(finiteDuration, seekableEnd)
      if (local >= 0 && windowEnd > 0.5 && local <= windowEnd - 0.5) {
        video.currentTime = local
        return true
      }
      setPlayerError('')
      toast('正在从该位置重新转码…', { duration: 2000 })
      setHlsStartSec(target)
      return true
    },
    [hlsStartSec, mode],
  )

  // ── 片头/片尾跳过：执行 ────────────────────────────────────────────────────
  // 跳到源时间轴上的绝对秒数。HLS 转码时不能直接写 currentTime：本地 HLS 交给
  // handleSeekAbsolute（超出缓冲窗口时会从头起一段新转码），云端 HLS 按转码起点换算。
  const seekPlaybackTo = useCallback(
    (absoluteSec: number) => {
      const video = ref.current
      if (!video) return
      const target = Math.max(0, absoluteSec)
      if (mode === 'hls') {
        if (hlsSource === 'local') {
          handleSeekAbsolute(target)
          return
        }
        selfSkipSeekRef.current = true
        video.currentTime = Math.max(0, target - hlsStartSec)
        return
      }
      selfSkipSeekRef.current = true
      video.currentTime = target
    },
    [handleSeekAbsolute, hlsSource, hlsStartSec, mode],
  )

  const showSkipNotice = useCallback(
    (notice: { text: string; startSec: number; kind: PlaybackSegmentKind }) => {
      setSkipNotice(notice)
      if (skipNoticeTimerRef.current) clearTimeout(skipNoticeTimerRef.current)
      skipNoticeTimerRef.current = setTimeout(() => setSkipNotice(null), SKIP_NOTICE_MS)
    },
    [],
  )

  // 执行一次跳过：片尾有下一集就直接进下一集，其余情况跳到区间终点。
  const performSkip = useCallback(
    (prompt: SkipPrompt) => {
      setDismissedSkipKinds((prev) => (prev.includes(prompt.kind) ? prev : [...prev, prompt.kind]))
      setActiveSkip(null)
      activeSkipKeyRef.current = ''
      const isOutro = prompt.kind === 'credits' || prompt.kind === 'preview'
      if (isOutro && nextEpisode) {
        toast.success(`正在播放下一集：${nextEpisodeTitle || '下一集'}`)
        playEpisode(nextEpisode)
        return
      }
      seekPlaybackTo(prompt.endSec)
    },
    [nextEpisode, nextEpisodeTitle, playEpisode, seekPlaybackTo],
  )

  const handleSkipClick = useCallback(() => {
    if (activeSkip) performSkip(activeSkip)
  }, [activeSkip, performSkip])

  const handleUndoSkip = useCallback(() => {
    const notice = skipNotice
    setSkipNotice(null)
    if (skipNoticeTimerRef.current) {
      clearTimeout(skipNoticeTimerRef.current)
      skipNoticeTimerRef.current = null
    }
    if (!notice) return
    // 撤销后本次播放不再自动跳这一段，否则会被立刻再跳一次；同时恢复按钮，
    // 用户改主意时还能手动跳。
    setAutoSuppressedKinds((prev) => (prev.includes(notice.kind) ? prev : [...prev, notice.kind]))
    setDismissedSkipKinds((prev) => prev.filter((kind) => kind !== notice.kind))
    seekPlaybackTo(notice.startSec)
  }, [seekPlaybackTo, skipNotice])

  // 自动跳过只针对片头（含回顾）：档案里的开关本身就叫「自动跳过片头」，而且自动
  // 跳到结尾会让没开自动连播的用户莫名其妙。片尾始终只提供按钮。
  useEffect(() => {
    if (!autoSkipIntro || !activeSkip) return
    if (activeSkip.kind !== 'intro' && activeSkip.kind !== 'recap') return
    if (autoSuppressedKinds.includes(activeSkip.kind)) return
    performSkip(activeSkip)
    showSkipNotice({
      text: skippedNoticeText(activeSkip.kind),
      startSec: activeSkip.startSec,
      kind: activeSkip.kind,
    })
  }, [activeSkip, autoSkipIntro, autoSuppressedKinds, performSkip, showSkipNotice])

  // 用户切换图片字幕时从当前位置创建新的 HLS 烧录任务；文本字幕只在网页层切换。
  const selectSubtitle = useCallback((index: number) => {
    const oldTrack = subtitleIndex >= 0 ? subs[subtitleIndex] : undefined
    const nextTrack = index >= 0 ? subs[index] : undefined
    if (nextTrack?.delivery === 'burn' && (directOnly || isDirectStream)) {
      toast.error('图片字幕需要开启 HLS 转码后才能显示')
      return
    }
    if (nextTrack?.delivery === 'burn') {
      // PGS 等图片字幕必须走本地 FFmpeg 烧录，不能交给云端 HLS。
      switchToLocalHLS(ref.current?.currentTime || 0)
    }
    const burnChanged =
      oldTrack?.delivery === 'burn' || nextTrack?.delivery === 'burn'
    if (burnChanged && mode === 'hls' && ref.current) {
      setHlsStartSec(hlsStartSec + (ref.current.currentTime || 0))
    }
    setSubtitleIndex(index)
    if (nextTrack?.delivery === 'burn' && mode !== 'hls' && !directOnly && !isDirectStream) {
      setHlsStartSec(ref.current?.currentTime || 0)
      setPlaybackMode('hls')
    }
  }, [directOnly, hlsStartSec, isDirectStream, mode, setPlaybackMode, subs, subtitleIndex, switchToLocalHLS])

  const selectPlaybackQuality = useCallback(
    (quality: PlaybackQuality) => {
      const video = ref.current
      const position = video?.currentTime || 0
      if (quality.source === 'original') {
        setCloudWaiting(false)
        setHlsSource('local')
        setPlaybackMode('direct')
        return
      }
      if (quality.source === 'cloud') {
        setSelectedQuality(quality.id)
        setHlsSource('cloud')
        if (quality.available) {
          setCloudWaiting(false)
          setCloudWaitMessage('')
          if (position > 2) pendingSeekRef.current = position
          setPlaybackMode('hls')
        } else {
          setCloudWaiting(true)
          setCloudWaitMessage(quality.note || `正在等待 115 转码 ${quality.label}…`)
          void startCloudTranscode(Number(quality.id) || 4)
        }
        return
      }
      setSelectedQuality(quality.id)
      setHlsSource('local')
      setCloudWaiting(false)
      setCloudWaitMessage('')
      // 本地档位不可用（宿主机没有可用的 ffmpeg）时不要进入必然失败的 HLS：
      // 保持直连播放并说明怎么修，避免播放器先打一个 500 再回退。
      if (!quality.available) {
        setPlaybackMode('direct')
        toast.error(quality.note || '当前无法进行 HLS 转码，请在「系统设置 → 常规」安装 ffmpeg 后重试')
        return
      }
      if (position > 2) setHlsStartSec(position)
      setPlaybackMode('hls')
    },
    [setPlaybackMode, startCloudTranscode],
  )

  const changeSubtitleChineseMode = useCallback(
    (nextMode: SubtitleChineseMode) => {
      subtitlePreferenceTouchedRef.current = true
      setSubtitleChineseMode(nextMode)

      const save = subtitlePreferenceSaveQueueRef.current.then(async () => {
        const user = await profileAPI.update({ subtitle_chinese_mode: nextMode })
        persistedSubtitleChineseModeRef.current = nextMode
        setAuthUser(user)
        toast.success('字幕转换偏好已保存，后续播放将自动沿用')
      })
      subtitlePreferenceSaveQueueRef.current = save.catch(() => undefined)
      void save.catch(() => {
        setSubtitleChineseMode((currentMode) =>
          currentMode === nextMode
            ? persistedSubtitleChineseModeRef.current
            : currentMode,
        )
        toast.error('字幕转换偏好保存失败，已恢复上次设置')
      })
    },
    [setAuthUser],
  )

  const handleVideoError = useCallback(() => {
    const video = ref.current
    if (mode !== 'direct') {
      setPlayerError('视频播放失败，请检查文件是否存在，或确认 ffmpeg 已正确配置。')
      toast.error('视频播放失败，请检查文件是否存在')
      return
    }

    if (retryingDirectRef.current) return

    const expectedSrc = mediaId
      ? new URL(streamURL(mediaId, { proxy: vr360DirectProxyRef.current }), window.location.href).href
      : ''
    const action = classifyDirectPlayError({
      errorCode: video?.error?.code,
      readyState: video?.readyState ?? 0,
      elementSrc: video?.src ?? '',
      expectedSrc,
      alreadyRetried: directRetryRef.current,
    })
    if (action === 'ignore') return

    if (action === 'retry' && video && mediaId) {
      directRetryRef.current = true
      retryingDirectRef.current = true
      try {
        video.load()
        void video.play().catch(() => undefined)
      } finally {
        retryingDirectRef.current = false
      }
      return
    }

    const fallbackDirectPlay = () => {
      if (modeRef.current !== 'direct') return
      const current = ref.current
      if (current && current.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA) return
      if (isRemoteEmbyID(mediaRef.current?.id) || isDirectStreamMedia(mediaRef.current)) {
        setPlayerError('直接播放失败。该媒体为远程 Emby 挂载直连播放（不进行转码）；当前浏览器可能不支持该视频编码或音频格式，建议使用外部播放器（如 PotPlayer / VLC / IINA）播放。')
        toast.error('直接播放失败，建议使用外部播放器')
      } else if (directOnly) {
        setPlayerError('直接播放失败。当前为「客户端直连解码」模式，宿主机不转码；请使用支持该编码/封装的播放器（如 Infuse / VLC / Emby 客户端）播放，或关闭直连解码模式。')
        toast.error('直接播放失败（客户端直连解码模式）')
      } else if (playbackInfo?.provider === 'cloud115') {
        const preferred =
          findPlaybackQualityById(playbackInfo, selectedQuality) ??
          findPlaybackQualityById(playbackInfo, playbackInfo.default_quality)
        if (preferred?.source === 'cloud' && preferred.available) {
          setCloudWaiting(false)
          setHlsSource('cloud')
          setPlaybackMode('hls')
        } else if (preferred?.source === 'cloud') {
          setHlsSource('cloud')
          setCloudWaiting(true)
          setCloudWaitMessage(preferred.note || `正在等待 115 转码 ${preferred.label}…`)
          void startCloudTranscode(Number(preferred.id) || 4)
        } else {
          switchToLocalHLS(current?.currentTime || 0)
        }
      } else if (hlsUnavailable) {
        setPlayerError('直接播放失败，且 HLS 转码不可用。请检查文件是否存在，或配置本机 ffmpeg 后使用 HLS 转码播放。')
        toast.error('直接播放失败，HLS 转码不可用')
      } else {
        toast.error('直接播放失败，切换到 HLS 转码')
        switchToLocalHLS(current?.currentTime || 0)
      }
    }

    clearFallbackTimer()
    fallbackTimerRef.current = setTimeout(fallbackDirectPlay, 1500)
  }, [
    clearFallbackTimer,
    directOnly,
    hlsUnavailable,
    mediaId,
    mode,
    playbackInfo,
    selectedQuality,
    setPlaybackMode,
    startCloudTranscode,
    switchToLocalHLS,
  ])

  const changeSubtitlePosition = useCallback((nextPosition: SubtitlePosition) => {
    setSubtitlePosition(nextPosition)
    saveSubtitlePosition(nextPosition)
  }, [])

  const changeSubtitleStyle = useCallback((nextStyle: SubtitleStylePreset) => {
    setSubtitleStyle(nextStyle)
    saveSubtitleStyle(nextStyle)
  }, [])

  const danmakuAutoTitle =
    danmakuInfo?.animeTitle ||
    media?.original_name?.trim() ||
    media?.title?.trim() ||
    ''

  // 竖屏手机走剧场布局：视频贴顶按 16:9 自适应，下方是标题/选集/简介。
  // 桌面端与横屏手机保持原来的全屏居中沉浸式布局。
  const isMobileTheater = useIsMobileTheater()

  return (
    <div className="relative flex h-full w-full flex-1 flex-col overflow-hidden bg-black">
      <PlayerTopBar
        title={topBarTitle}
        subtitle={topBarSubtitle}
        directOnly={directOnly}
        isDirectStream={isDirectStream}
        directStreamLabel={isRemoteEmbyID(media?.id) ? 'Emby 直连播放' : undefined}
        mode={mode}
        onBack={goBack}
      />
      <PlayerVideoStage
        theater={isMobileTheater}
        media={media}
        loadError={loadError}
        playerError={playerError}
        subs={subs}
        subtitleIndex={subtitleIndex}
        onSelectSubtitle={selectSubtitle}
        subtitleChineseMode={subtitleChineseMode}
        onSubtitleChineseModeChange={changeSubtitleChineseMode}
        subtitlePosition={subtitlePosition}
        onSubtitlePositionChange={changeSubtitlePosition}
        subtitleStyle={subtitleStyle}
        onSubtitleStyleChange={changeSubtitleStyle}
        videoRef={ref}
        onVideoError={handleVideoError}
        playerVolume={playerVolume}
        onPlayerVolumeChange={changePlayerVolume}
        onPlayerVolumeCommit={commitPlayerVolume}
        playerPlaybackRate={playerPlaybackRate}
        onPlayerPlaybackRateChange={changePlayerPlaybackRate}
        onPlayerPlaybackRateCommit={commitPlayerPlaybackRate}
        danmakuEnabled={danmakuEnabled}
        danmakuOpacity={danmakuOpacity}
        danmakuFontSize={danmakuFontSize}
        danmakuArea={danmakuArea}
        danmakuSearch={danmakuSearch}
        danmakuEpisodeId={danmakuEpisodeId}
        danmakuSearchTrigger={danmakuSearchTrigger}
        danmakuOpen={danmakuOpen}
        onOpenDanmaku={toggleDanmakuOpen}
        onDanmakuLoaded={danmakuLoaded}
        onDanmakuCandidates={danmakuGotCandidates}
        onDanmakuAlternatives={danmakuGotAlternatives}
        hasPrevEpisode={Boolean(prevEpisode)}
        hasNextEpisode={Boolean(nextEpisode)}
        onPrevEpisode={handlePrevEpisode}
        onNextEpisode={handleNextEpisode}
        prevEpisodeTitle={prevEpisodeTitle}
        nextEpisodeTitle={nextEpisodeTitle}
        playlistOpen={playlistOpen}
        hasPlaylist={playlistEpisodes.length > 1 || currentVersions.length > 1}
        hasVersions={currentVersions.length > 1}
        onTogglePlaylist={isMobileTheater ? openPlaylistEntry : togglePlaylistOpen}
        knownDuration={media?.duration_sec || 0}
        streamOffset={mode === 'hls' && hlsSource === 'local' ? hlsStartSec : 0}
        onSeekAbsolute={mode === 'hls' && hlsSource === 'local' ? handleSeekAbsolute : undefined}
        qualities={qualityOptions}
        qualityLabel={qualityLabel}
        selectedQuality={selectedQuality}
        onSelectQuality={selectPlaybackQuality}
        showQuality={showQuality}
        playbackModeLabel={playbackModeLabel}
        onTogglePlaybackMode={canTogglePlaybackMode ? toggleMode : undefined}
        waiting={cloudWaiting}
        waitingMessage={cloudWaitMessage}
        vr360={vr360}
        vr360Detected={vr360Detected}
        onToggleVr360={toggleVr360}
        onVr360ProfileChange={changeVr360Profile}
        onVr360Error={handleVr360Error}
        showVr360Guide={Boolean(vr360) && vr360GuideSeen === false}
        onDismissVr360Guide={dismissVr360Guide}
        skipPrompt={activeSkip}
        onSkipPrompt={handleSkipClick}
        skipNotice={skipNotice ? { text: skipNotice.text } : null}
        onUndoSkip={handleUndoSkip}
        playlistPanel={
          isMobileTheater
            ? undefined
            : <PlayerPlaylistPanel
                open={playlistOpen}
                onClose={() => setPlaylistOpen(false)}
                currentMediaId={media?.id ?? ''}
                currentVersions={currentVersions}
                episodes={playlistEpisodes}
                onSelectEpisode={playEpisode}
                onSelectVersion={switchVersion}
              />
        }
        danmakuPanel={
          <PlayerDanmakuPanel
            theater={isMobileTheater}
            open={danmakuOpen}
            onClose={() => setDanmakuOpen(false)}
            enabled={danmakuEnabled}
            onToggleEnabled={danmakuChangeEnabled}
            search={danmakuSearch ?? ''}
            onSearch={searchDanmaku}
            searching={danmakuSearching}
            area={danmakuArea}
            onAreaChange={setDanmakuArea}
            onAreaCommit={commitDanmakuArea}
            opacity={danmakuOpacity}
            onOpacityChange={setDanmakuOpacity}
            onOpacityCommit={commitDanmakuOpacity}
            fontSize={danmakuFontSize}
            onFontSizeChange={setDanmakuFontSize}
            onFontSizeCommit={commitDanmakuFontSize}
            source={danmakuSource}
            appId={danmakuAppID}
            appKeyConfigured={danmakuAppKeyConfigured}
            settingsSaving={playerSettingsSaving}
            onSaveAdvanced={saveDanmakuAdvanced}
            candidates={danmakuCandidates}
            alternatives={danmakuAlternatives}
            mergeSources={danmakuMergeSources}
            onMergeSourcesChange={danmakuChangeMergeSources}
            mergeSaving={danmakuMergeSaving}
            selectedSource={danmakuSelectedSource}
            autoMatchTitle={danmakuAutoTitle}
            danmakuInfo={danmakuInfo}
            onSelectEpisode={danmakuSelectEpisode}
            onResetAuto={danmakuResetAuto}
          />
        }
      />
      {isMobileTheater ? (
        <PlayerMobileTheaterInfo
          media={media?.id === id ? media : null}
          title={topBarTitle}
          subtitle={topBarSubtitle}
          playbackModeLabel={playbackModeLabel}
          qualityLabel={showQuality ? qualityLabel : undefined}
          episodes={playlistEpisodes}
          currentMediaId={media?.id ?? ''}
          currentEpisodeIndex={currentEpisodeIndex}
          currentVersions={currentVersions}
          onSelectEpisode={playEpisode}
          onSelectVersion={switchVersion}
          playlistOpen={playlistOpen}
          onTogglePlaylist={togglePlaylistOpen}
          revealToken={episodeRevealToken}
          playlistPanel={
            <PlayerPlaylistPanel
              inline
              open={playlistOpen}
              onClose={() => setPlaylistOpen(false)}
              currentMediaId={media?.id ?? ''}
              currentVersions={currentVersions}
              episodes={playlistEpisodes}
              onSelectEpisode={playEpisode}
              onSelectVersion={switchVersion}
            />
          }
        />
      ) : null}
    </div>
  )
}

function formatEpisodeDisplay(ep: Media, siblings: Media[]): string {
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

function findPlaybackQualityById(info: PlaybackInfo | null, id: string): PlaybackQuality | undefined {
  if (!info || !id) return undefined
  return [...(info.cloud_qualities ?? []), ...(info.local_qualities ?? [])].find(
    (quality) => quality.id === id,
  )
}

function defaultLocalQualityId(info: PlaybackInfo | null): string {
  const qualities = info?.local_qualities ?? []
  for (const id of ['1080', '720', '480', 'source']) {
    if (qualities.some((quality) => quality.id === id)) return id
  }
  return qualities[0]?.id ?? '720'
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
