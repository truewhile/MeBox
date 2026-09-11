import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import type Hls from 'hls.js'
import toast from 'react-hot-toast'

import { mediaAPI, libraryAPI } from '../api/library'
import { hlsURL, stopHLSJob, streamURL } from '../api/client'
import { danmakuAPI, type DanmakuAnime, type DanmakuLoadedInfo } from '../api/danmaku'
import { playbackAPI } from '../api/playback'
import { subtitlesAPI, type SubtitleTrack } from '../api/subtitles'
import { systemAPI } from '../api/system'
import { profileAPI } from '../api/profile'
import { useAuthStore } from '../stores/auth'
import type { Media } from '../types'
import { getSeriesKey, seriesTitleFromPath } from '../utils/groupSeries'
import { isRemoteEmbyID } from '../utils/remoteEmby'
import {
  normalizeSubtitleChineseMode,
  type SubtitleChineseMode,
} from '../utils/subtitleChinese'
import { pickPlayerMode, needsTranscodeForBrowser, isDirectStreamMedia, type PlayerMode } from './playerPageModel'
import { classifyDirectPlayError } from './directPlayError'
import { apiErrorMessage } from './StrmManagePage'
import { PlayerTopBar } from './PlayerTopBar'
import { PlayerVideoStage } from './PlayerVideoStage'
import { PlayerDanmakuPanel } from '../components/PlayerDanmakuPanel'
import { PlayerPlaylistPanel } from '../components/PlayerPlaylistPanel'
import { MediaVersionSwitcher } from '../components/MediaVersionSwitcher'
import { mediaVersionsOf } from '../utils/mediaVersion'

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
export function PlayerPage() {
  const { id = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const location = useLocation()

  const ref = useRef<HTMLVideoElement>(null)
  const hlsRef = useRef<Hls | null>(null)
  const lastSentRef = useRef(0)
  const directRetryRef = useRef(false)
  const retryingDirectRef = useRef(false)
  const fallbackTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const [media, setMedia] = useState<Media | null>(null)
  const [mode, setMode] = useState<PlayerMode>('direct')
  const [subs, setSubs] = useState<SubtitleTrack[]>([])
  const [subtitleIndex, setSubtitleIndex] = useState<number>(0)
  const authUser = useAuthStore((state) => state.user)
  const setAuthUser = useAuthStore((state) => state.setUser)
  const [subtitleChineseMode, setSubtitleChineseMode] =
    useState<SubtitleChineseMode>(() =>
      normalizeSubtitleChineseMode(authUser?.subtitle_chinese_mode),
    )
  const persistedSubtitleChineseModeRef = useRef(subtitleChineseMode)
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
  // 已加载弹幕的元数据信息（番剧名、单集名、条数、匹配模式等）。
  const [danmakuInfo, setDanmakuInfo] = useState<DanmakuLoadedInfo | null>(null)
  // 用户当前选定的弹幕来源描述（面板中展示）。
  const [danmakuSelectedSource, setDanmakuSelectedSource] = useState('')
  const [danmakuOpacity, setDanmakuOpacity] = useState(1)
  const [danmakuFontSize, setDanmakuFontSize] = useState(24)
  const [danmakuArea, setDanmakuArea] = useState(1)

  // 选集 / 播放列表状态
  const [playlistEpisodes, setPlaylistEpisodes] = useState<Media[]>([])
  const [playlistOpen, setPlaylistOpen] = useState(false)

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

  // 读取宿主机已保存的弹幕参数作为面板初始值（无 admin 权限也可读）。
  useEffect(() => {
    danmakuAPI
      .config()
      .then((cfg) => {
        setDanmakuEnabled(cfg.enabled)
        setDanmakuOpacity(Number(cfg.opacity) || 1)
        setDanmakuFontSize(Number(cfg.font_size) || 24)
        setDanmakuArea(Number(cfg.area) || 1)
      })
      .catch(() => undefined)
  }, [])

  // 用户手动搜索：带关键词重新拉取（search=null 时按视频名）。
  // loading 状态由 DanmakuStage 拉取完成回调（onLoaded）驱动。
  const searchDanmaku = useCallback((kw: string) => {
    setDanmakuSearching(true)
    setDanmakuCandidates([])
    setDanmakuEpisodeId(null)
    setDanmakuInfo(null)
    setDanmakuSearch(kw || null)
    setDanmakuSearchTrigger((prev) => prev + 1)
  }, [])

  const danmakuLoaded = useCallback((info: DanmakuLoadedInfo | null) => {
    setDanmakuSearching(false)
    setDanmakuInfo(info)
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
    setDanmakuSearching(true)
    // 展示当前所选来源（面板标题处可见）。
    setDanmakuSelectedSource(episodeTitle ? `${animeTitle}・${episodeTitle}` : animeTitle)
    setDanmakuSearchTrigger((prev) => prev + 1)
  }, [])

  // 回到自动匹配（清除用户手动选择）。
  const danmakuResetAuto = useCallback(() => {
    setDanmakuEpisodeId(null)
    setDanmakuCandidates([])
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
    setHlsStartSec(0)
    setDanmakuEpisodeId(null)
    setDanmakuCandidates([])
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

  // Wire up the actual <video> element when we know the mode.
  // Depend on media.id (not the media object): refreshing duration after
  // MANIFEST_PARSED must not remount HLS or it storms EnsureJob / DELETE.
  const mediaId = media?.id

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
      const url = hlsURL(mediaId, hlsStartSec, activeBurnedSubtitleStream)
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
              setHlsUnavailable(true)
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
      const url = streamURL(mediaId)
      const absoluteURL = new URL(url, window.location.href).href
      // 其它异步播放器状态更新不应重启同一个直连请求；STRM 的重定向/换链
      // 比本地文件慢，重启请求可能产生一个短暂但会触发 onError 的中断。
      if (video.src !== absoluteURL) {
        directRetryRef.current = false
        clearFallbackTimer()
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
    hlsUnavailable,
    hlsStartSec,
    mediaId,
    mode,
    setPlaybackMode,
    teardownHls,
  ])

  // Stop host ffmpeg when leaving this HLS player. The keepalive request also
  // survives route navigation while the component is being torn down.
  useEffect(() => {
    if (!mediaId || mode !== 'hls') return
    return () => {
      stopHLSJob(mediaId)
    }
  }, [mediaId, mode])

  // React cleanup is not guaranteed when a tab/window closes. pagehide fires
  // while the document can still dispatch a keepalive request.
  useEffect(() => {
    if (!mediaId || mode !== 'hls') return
    const stopOnPageExit = () => stopHLSJob(mediaId)
    window.addEventListener('pagehide', stopOnPageExit)
    return () => window.removeEventListener('pagehide', stopOnPageExit)
  }, [mediaId, mode])

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

  // Persist resume position every 10 seconds while playing, and immediately upon pause/unmount.
  useEffect(() => {
    if (!id || !ref.current) return
    const video = ref.current
    const absolutePositionMs = () => {
      const currentStartSec = modeRef.current === 'hls' ? hlsStartSecRef.current : 0
      return Math.floor((currentStartSec + (video.currentTime || 0)) * 1000)
    }
    const absoluteDurationMs = () => {
      const currentStartSec = modeRef.current === 'hls' ? hlsStartSecRef.current : 0
      const mediaDur = mediaRef.current?.duration_sec || 0
      return Math.floor(Math.max(mediaDur, currentStartSec + (video.duration || 0)) * 1000)
    }
    const handler = () => {
      const now = Date.now()
      if (now - lastSentRef.current < 10_000) return
      lastSentRef.current = now
      const positionMs = absolutePositionMs()
      const durationMs = absoluteDurationMs()
      if (positionMs > 0 && mediaRef.current) {
        playbackAPI.recordProgress(mediaRef.current.id, positionMs, durationMs).catch(() => undefined)
      }
    }
    video.addEventListener('timeupdate', handler)
    video.addEventListener('pause', handler)
    return () => {
      video.removeEventListener('timeupdate', handler)
      video.removeEventListener('pause', handler)
      const positionMs = absolutePositionMs()
      const durationMs = absoluteDurationMs()
      if (positionMs > 0 && mediaRef.current) {
        playbackAPI.recordProgress(mediaRef.current.id, positionMs, durationMs).catch(() => undefined)
      }
    }
  }, [id])

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

  const currentEpisodeIndex = useMemo(() => {
    if (!media || playlistEpisodes.length === 0) return -1
    return playlistEpisodes.findIndex((e) => e.id === media.id)
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

  // URL 已切换但新媒体尚未返回时，不渲染上一条媒体遗留的版本入口。
  const versionList = useMemo(
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

  // ESC = back 或关闭浮层，[ / ] 或 Shift+P / Shift+N 切换上一集/下一集
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
      setHlsStartSec(0)
    }
    setPlaybackMode(next)
  }, [isDirectStream, mode, setPlaybackMode])

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

  // 用户切换图片字幕时从当前位置创建新的 HLS 烧录任务；文本字幕只在网页层切换。
  const selectSubtitle = useCallback((index: number) => {
    const oldTrack = subtitleIndex >= 0 ? subs[subtitleIndex] : undefined
    const nextTrack = index >= 0 ? subs[index] : undefined
    if (nextTrack?.delivery === 'burn' && (directOnly || isDirectStream)) {
      toast.error('图片字幕需要开启 HLS 转码后才能显示')
      return
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
  }, [directOnly, hlsStartSec, isDirectStream, mode, setPlaybackMode, subs, subtitleIndex])

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

    const expectedSrc = mediaId ? new URL(streamURL(mediaId), window.location.href).href : ''
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
      } else if (hlsUnavailable) {
        setPlayerError('直接播放失败，且 HLS 转码不可用。请检查文件是否存在，或配置本机 ffmpeg 后使用 HLS 转码播放。')
        toast.error('直接播放失败，HLS 转码不可用')
      } else {
        toast.error('直接播放失败，切换到 HLS 转码')
        setPlaybackMode('hls')
      }
    }

    clearFallbackTimer()
    fallbackTimerRef.current = setTimeout(fallbackDirectPlay, 1500)
  }, [clearFallbackTimer, directOnly, hlsUnavailable, mediaId, mode, setPlaybackMode])

  const danmakuAutoTitle =
    danmakuInfo?.animeTitle ||
    media?.original_name?.trim() ||
    media?.title?.trim() ||
    ''

  return (
    <div className="relative flex h-full w-full flex-1 flex-col overflow-hidden bg-black">
      <PlayerTopBar
        directOnly={directOnly}
        isDirectStream={isDirectStream}
        directStreamLabel={isRemoteEmbyID(media?.id) ? 'Emby 直连播放' : undefined}
        mode={mode}
        onBack={goBack}
        onToggleMode={toggleMode}
      />
      {versionList.length > 1 && (
        <div className="pointer-events-none absolute inset-x-0 top-16 z-20 flex justify-center px-4 sm:top-20">
          <div className="pointer-events-auto max-w-3xl rounded-2xl border border-white/15 bg-black/70 px-3 py-2 shadow-xl backdrop-blur">
            <MediaVersionSwitcher media={media!} mode="player" onSelect={switchVersion} className="text-white" />
          </div>
        </div>
      )}
      <PlayerVideoStage
        media={media}
        loadError={loadError}
        playerError={playerError}
        subs={subs}
        subtitleIndex={subtitleIndex}
        onSelectSubtitle={selectSubtitle}
        subtitleChineseMode={subtitleChineseMode}
        onSubtitleChineseModeChange={changeSubtitleChineseMode}
        videoRef={ref}
        onVideoError={handleVideoError}
        danmakuEnabled={danmakuEnabled}
        danmakuOpacity={danmakuOpacity}
        danmakuFontSize={danmakuFontSize}
        danmakuArea={danmakuArea}
        danmakuSearch={danmakuSearch}
        danmakuEpisodeId={danmakuEpisodeId}
        danmakuSearchTrigger={danmakuSearchTrigger}
        danmakuOpen={danmakuOpen}
        onToggleDanmaku={toggleDanmakuOpen}
        onDanmakuLoaded={danmakuLoaded}
        onDanmakuCandidates={danmakuGotCandidates}
        hasPrevEpisode={Boolean(prevEpisode)}
        hasNextEpisode={Boolean(nextEpisode)}
        onPrevEpisode={handlePrevEpisode}
        onNextEpisode={handleNextEpisode}
        prevEpisodeTitle={prevEpisodeTitle}
        nextEpisodeTitle={nextEpisodeTitle}
        playlistOpen={playlistOpen}
        hasPlaylist={playlistEpisodes.length > 0}
        onTogglePlaylist={togglePlaylistOpen}
        knownDuration={media?.duration_sec || 0}
        streamOffset={mode === 'hls' ? hlsStartSec : 0}
        onSeekAbsolute={mode === 'hls' ? handleSeekAbsolute : undefined}
        playlistPanel={
          <PlayerPlaylistPanel
            open={playlistOpen}
            onClose={() => setPlaylistOpen(false)}
            currentMediaId={media?.id ?? ''}
            episodes={playlistEpisodes}
            onSelectEpisode={playEpisode}
          />
        }
        danmakuPanel={
          <PlayerDanmakuPanel
            open={danmakuOpen}
            onClose={() => setDanmakuOpen(false)}
            enabled={danmakuEnabled}
            onToggleEnabled={setDanmakuEnabled}
            search={danmakuSearch ?? ''}
            onSearch={searchDanmaku}
            searching={danmakuSearching}
            area={danmakuArea}
            onAreaChange={setDanmakuArea}
            opacity={danmakuOpacity}
            onOpacityChange={setDanmakuOpacity}
            fontSize={danmakuFontSize}
            onFontSizeChange={setDanmakuFontSize}
            candidates={danmakuCandidates}
            selectedSource={danmakuSelectedSource}
            autoMatchTitle={danmakuAutoTitle}
            danmakuInfo={danmakuInfo}
            onSelectEpisode={danmakuSelectEpisode}
            onResetAuto={danmakuResetAuto}
          />
        }
      />
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
