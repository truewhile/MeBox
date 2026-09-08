import { useEffect, useRef, useState } from 'react'
import type { PointerEvent, ReactNode, RefObject } from 'react'

import { subtitlesAPI, type SubtitleTrack } from '../api/subtitles'
import { type DanmakuAnime, type DanmakuLoadedInfo } from '../api/danmaku'
import type { Media } from '../types'
import { DanmakuStage } from '../components/DanmakuStage'
import { PlayerControls } from '../components/PlayerControls'

type SubtitleCue = {
  startTime: number
  endTime: number
  text: string
}

function parseVTTTimestamp(value: string): number {
  const parts = value.trim().replace(',', '.').split(':')
  if (parts.length !== 2 && parts.length !== 3) return Number.NaN
  const seconds = Number(parts.pop())
  const minutes = Number(parts.pop())
  const hours = parts.length > 0 ? Number(parts.pop()) : 0
  if (![hours, minutes, seconds].every(Number.isFinite)) return Number.NaN
  return hours * 3600 + minutes * 60 + seconds
}

function parseWebVTTCues(body: string): SubtitleCue[] {
  const blocks = body
    .replace(/^\uFEFF/, '')
    .replace(/\r\n?/g, '\n')
    .split(/\n{2,}/)
  const cues: SubtitleCue[] = []

  for (const block of blocks) {
    const lines = block.split('\n')
    const timingIndex = lines.findIndex((line) => line.includes('-->'))
    if (timingIndex < 0) continue

    const [rawStart, rawEnd] = lines[timingIndex].split('-->', 2)
    const startTime = parseVTTTimestamp(rawStart)
    const endTime = parseVTTTimestamp(rawEnd.trim().split(/\s+/, 1)[0])
    const text = lines.slice(timingIndex + 1).join('\n').trim()
    if (Number.isFinite(startTime) && Number.isFinite(endTime) && endTime >= startTime && text) {
      cues.push({ startTime, endTime, text })
    }
  }

  return cues
}

function uniqueSubtitleTexts(texts: string[]): string[] {
  const unique = [...new Set(texts.map((text) => text.trim()).filter(Boolean))]
  // ponytail: WebVTT cannot preserve ASS layers/positions. Keep at most two
  // simultaneous blocks for bilingual subtitles; use libass if full ASS
  // typesetting support is added later.
  return unique.slice(-2)
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
  videoRef: RefObject<HTMLVideoElement>
  onVideoError: () => void
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
}

export function PlayerVideoStage({
  media,
  loadError,
  playerError,
  subs,
  subtitleIndex,
  onSelectSubtitle,
  videoRef,
  onVideoError,
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
}: PlayerVideoStageProps) {
  const stageRef = useRef<HTMLDivElement>(null)
  const [videoRatio, setVideoRatio] = useState<number | null>(null)
  const [stageRect, setStageRect] = useState<{ width: number; height: number } | null>(null)
  const [controlsVisible, setControlsVisible] = useState(true)
  const revealControlsOnlyRef = useRef(false)
  // 当前展示的字幕文本（由自定义字幕层渲染，100% 透明无黑框）
  const [activeCueText, setActiveCueText] = useState<string>('')
  // 独立保存完整 WebVTT 时间轴。HLS seek 会替换媒体源，Chromium 此时可能清空
  // <track>.track.cues；独立时间轴不受 MediaSource 重挂载和轨道 mode 切换影响。
  const [subtitleTimeline, setSubtitleTimeline] = useState<{
    path: string
    cues: SubtitleCue[]
  } | null>(null)
  // 直连 302 尚未完成时插入 <track> 会中断加载并误报播放失败；等 canplay 再挂。
  const [tracksArmed, setTracksArmed] = useState(false)

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
    if (!media || subtitleIndex < 0 || !selectedTrack || selectedTrack.delivery !== 'webvtt') {
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
  }, [media, subs, subtitleIndex])

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
    revealControlsOnlyRef.current = event.pointerType === 'touch' && !controlsVisible
  }
  const handleStageClick = () => {
    if (revealControlsOnlyRef.current) {
      revealControlsOnlyRef.current = false
      setControlsVisible(true)
      return
    }
    revealControlsOnlyRef.current = false
    togglePlay()
  }
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
      selectedTrack.delivery === 'burn'
    ) {
      setActiveCueText('')
      return
    }

    const updateCue = () => {
      const absoluteTime = video.currentTime + (streamOffset ?? 0)
      if (subtitleTimeline?.path === selectedTrack.path) {
        const texts = uniqueSubtitleTexts(
          subtitleTimeline.cues
            .filter((cue) => absoluteTime >= cue.startTime && absoluteTime <= cue.endTime)
            .map((cue) => cue.text),
        )
        setActiveCueText(texts.join('\n'))
        return
      }

      const selectedEl = video.querySelector<HTMLTrackElement>(
        `track[data-subtitle-index="${subtitleIndex}"]`,
      )
      const tt = selectedEl?.track
      if (!tt) {
        setActiveCueText('')
        return
      }

      // 优先从浏览器 activeCues 中取当前文本；若浏览器在 hidden 模式下延迟触发 cuechange，
      // 则从 tt.cues 中根据 video.currentTime 实时匹配当前字幕，确保初次加载无感立即可见。
      const texts: string[] = []
      if ((!streamOffset || streamOffset <= 0.05) && tt.activeCues && tt.activeCues.length > 0) {
        for (let i = 0; i < tt.activeCues.length; i++) {
          const cue = tt.activeCues[i] as VTTCue
          if (cue && cue.text) texts.push(cue.text)
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
            texts.push(cue.text)
          }
        }
      }
      setActiveCueText(uniqueSubtitleTexts(texts).join('\n'))
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
  }, [subtitleIndex, subs, videoRef, media, streamOffset, subtitleTimeline, tracksArmed])

  // 根据视频画面宽高比与舞台宽高比，确定视频在哪个轴向撑满 100%
  const isWiderThanStage =
    videoRatio && stageRect && stageRect.height > 0
      ? videoRatio > stageRect.width / stageRect.height
      : true

  const wrapperStyle = videoRatio
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
      onClick={handleStageClick}
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
                subs.map((track, index) =>
                  track.delivery === 'burn' ? null : (
                    <track
                      key={track.path}
                      data-subtitle-index={index}
                      kind="subtitles"
                      src={subtitlesAPI.url(media.id, track.path)}
                      srcLang={track.lang}
                      label={track.label || track.lang}
                    />
                  ),
                )}
            </video>
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
            />
            {/* 自定义沉浸式字幕层：纯透明背景 + 柔和阴影，完全消除浏览器原生黑框 */}
            {activeCueText ? (
              <div className="pointer-events-none absolute inset-x-0 bottom-4 sm:bottom-6 md:bottom-8 z-10 flex justify-center text-center px-4">
                <span
                  className="inline-block max-w-[92%] whitespace-pre-line text-center font-sans font-medium text-white text-base sm:text-lg md:text-xl lg:text-2xl select-none"
                  style={{
                    textShadow:
                      '0 1px 3px rgba(0, 0, 0, 0.95), 0 0 8px rgba(0, 0, 0, 0.85), 0 0 16px rgba(0, 0, 0, 0.65)',
                    lineHeight: 1.35,
                  }}
                >
                  {activeCueText}
                </span>
              </div>
            ) : null}
          </div>
          <PlayerControls
            videoRef={videoRef}
            uiVisible={controlsVisible}
            onUiVisibleChange={setControlsVisible}
            subs={subs}
            subtitleIndex={subtitleIndex}
            onSelectSubtitle={onSelectSubtitle}
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
          />
          {danmakuPanel}
          {playlistPanel}
        </>
      ) : loadError ? (
        <p className="max-w-[92%] text-center text-sm text-rose-400">{loadError}</p>
      ) : (
        <p className="text-sand-500">加载中…</p>
      )}
      {playerError ? (
        <div className="absolute bottom-20 left-1/2 w-[min(92vw,720px)] -translate-x-1/2 rounded-2xl border border-white/15 bg-black/75 px-5 py-4 text-sm text-white shadow-2xl backdrop-blur">
          {playerError}
        </div>
      ) : null}
    </div>
  )
}
