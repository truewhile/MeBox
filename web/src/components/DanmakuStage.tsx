import { useEffect, useRef } from 'react'
import { create, type Manager, type ManagerPlugin } from 'danmu'

import { danmakuAPI, type DanmakuAnime, type DanmakuLoadedInfo } from '../api/danmaku'
import type { Media } from '../types'
import { parseDanmaku, type Comment } from '../utils/parseDanmaku'

// DanmakuStage overlays danmu (the same engine behind dan-player) onto the
// <video> element. The upstream payload (dandanplay Bilibili-format XML) is
// fetched by the backend keyed on the video's name; parsing happens here.
//
// Loading follows danmaku-anywhere's flow: the backend auto-resolves when the
// search hits exactly one anime; when several anime match it returns
// candidates and the player shows a picker (`onCandidates`). After the user
// picks, `episodeId` forces that library.
//
// The component is controlled by the player page: enabled toggles loading,
// opacity / fontSize / area are applied live via the engine's setters, and
// `search` re-fetches with a custom keyword (null = use the media's own name).
type DanmakuStageProps = {
  media: Media | null
  videoRef: React.RefObject<HTMLVideoElement>
  enabled?: boolean
  opacity?: number
  fontSize?: number
  area?: number
  /** Custom search keyword; null/undefined uses the video name. */
  search?: string | null
  /** Explicit danmaku library chosen by the user; null = auto-resolve. */
  episodeId?: number | string | null
  /** Counter or token changed to trigger refetch even when search stays identical. */
  searchTrigger?: number
  /** Called after each fetch attempt (success or error) finishes with metadata. */
  onLoaded?: (info: DanmakuLoadedInfo | null) => void
  /** Called when multiple anime matched and the user must pick one. */
  onCandidates?: (candidates: DanmakuAnime[]) => void
}

// Average of the engine's durationRange (ms). Used to compute how far a
// comment should already have flown when it is pushed late after a seek.
const TRAVEL_MS = 5000
// How long a fixed top/bottom comment stays visible.
const FIXED_HOLD_MS = 4500
const MAX_VIEW = 200

export function DanmakuStage({
  media,
  videoRef,
  enabled = true,
  opacity = 1,
  fontSize = 24,
  area = 1,
  search = null,
  episodeId = null,
  searchTrigger = 0,
  onLoaded,
  onCandidates,
}: DanmakuStageProps) {
  const holderRef = useRef<HTMLDivElement>(null)
  const managerRef = useRef<Manager<Comment> | null>(null)
  const fontPxRef = useRef(fontSize)
  const liveRef = useRef({ opacity, area })

  // Live knobs in refs so the engine effect reads current values without
  // depending on them (avoiding engine recreation on every slider move).
  useEffect(() => {
    fontPxRef.current = fontSize
    liveRef.current = { opacity, area }
  }, [opacity, area, fontSize])

  // Engine lifecycle: create once per media + video element, keep alive while
  // danmaku is enabled.
  useEffect(() => {
    const holder = holderRef.current
    const video = videoRef.current
    if (!holder || !media || !video || !enabled) return

    let disposed = false
    let frozen = false
    let nextIndex = 0
    let comments: Comment[] = []

    const renderer: ManagerPlugin<Comment> = {
      name: 'danmaku-stage-render',
      $createNode: (dm, node) => {
        const base = fontPxRef.current
        node.textContent = dm.data.text
        node.style.color = dm.data.color
        node.style.fontSize = `${Math.round(base * dm.data.size)}px`
        node.style.fontWeight = '600'
        node.style.textShadow = '0 1px 2px rgba(0,0,0,.7)'
        node.style.whiteSpace = 'nowrap'
        node.style.lineHeight = '1.2'
        node.style.userSelect = 'none'
        node.style.pointerEvents = 'none'
        node.style.willChange = 'transform'
      },
    }

    const manager = create<Comment>({
      plugin: renderer,
      mode: 'strict',
      interval: 250,
      gap: 4,
      trackHeight: `${Math.max(20, Math.round(fontPxRef.current * 1.4))}px`,
      durationRange: [Math.round(TRAVEL_MS * 0.8), Math.round(TRAVEL_MS * 1.2)],
      direction: 'right',
      limits: { view: MAX_VIEW, stash: Infinity },
    })
    managerRef.current = manager
    manager.mount(holder)
    // 弹幕层不拦截播放器控制栏的点击。
    holder.style.pointerEvents = 'none'

    // 监听 holder 尺寸变化（全屏/退出全屏/窗口缩放），实时重置弹道与容器边界
    const ro = new ResizeObserver(() => {
      if (!disposed && managerRef.current) {
        managerRef.current.format()
      }
    })
    ro.observe(holder)

    const applyLiveSettings = () => {
      const { opacity: liveOpacity, area: liveArea } = liveRef.current
      manager.setOpacity(liveOpacity)
      // 显示区域：area ∈ (0,1] 表示弹幕占视频高度的比例。
      const a = Math.min(1, Math.max(0.05, liveArea))
      manager.setArea({ y: { start: 0, end: `${Math.round(a * 100)}%` } })
    }
    applyLiveSettings()

    const loadDanmaku = async () => {
      let loadedInfo: DanmakuLoadedInfo | null = null
      comments = []
      nextIndex = 0
      manager.clear()
      try {
        const res = await danmakuAPI.fetch(media.id, {
          kw: search ?? undefined,
          episodeId: episodeId ?? undefined,
        })
        if (disposed) return
        if (res.candidates && res.candidates.length > 0) {
          // 多番剧命中：交回播放器展示候选让用户选择（disambiguation）。
          comments = []
          onCandidates?.(res.candidates)
          return
        }
        if (res.enabled) {
          comments = parseDanmaku(res.raw || '', res.source_type)
            .filter((c) => Number.isFinite(c.time) && c.time >= 0)
            .sort((a, b) => a.time - b.time)
          nextIndex = 0
          loadedInfo = {
            animeTitle: res.anime_title,
            episodeTitle: res.episode_title,
            episodeId: res.episode_id ?? (episodeId ? Number(episodeId) || episodeId : undefined),
            matchMode: res.match_mode,
            totalCount: comments.length,
            sourceType: res.source_type,
          }
        } else {
          comments = []
          loadedInfo = {
            totalCount: 0,
          }
        }
      } catch {
        // 拉取失败时静默关闭弹幕，不打断播放。
        comments = []
        loadedInfo = {
          totalCount: 0,
        }
      } finally {
        if (!disposed) onLoaded?.(loadedInfo)
      }
    }

    const pushFlex = (comment: Comment, progress?: number) => {
      const isTop = comment.mode === 'top'
      manager.pushFlexibleDanmaku(comment, {
        duration: FIXED_HOLD_MS,
        direction: 'none',
        progress,
        position: (dm, container) => ({
          x: Math.max(4, (container.width - dm.getWidth()) / 2),
          y: isTop
            ? Math.max(4, Math.round(fontPxRef.current / 2))
            : Math.max(4, container.height - dm.getHeight() - 4),
        }),
      })
    }

    // 推入所有时间已到的弹幕；跳转后 `progress` 让弹幕从屏幕中部出现。
    const pushDue = (now: number) => {
      while (nextIndex < comments.length && comments[nextIndex].time <= now) {
        const c = comments[nextIndex]
        const elapsed = now - c.time
        if (elapsed <= TRAVEL_MS / 1000) {
          const progress = Math.min(0.95, Math.max(0, elapsed / (TRAVEL_MS / 1000)))
          if (c.mode === 'scroll') {
            manager.push(c, { progress })
          } else {
            pushFlex(c, progress)
          }
        }
        // 已飞出屏幕（超过 TRAVEL_MS）的弹幕直接丢弃，不进入队列。
        nextIndex++
      }
    }

    let raf = 0
    const tick = () => {
      raf = requestAnimationFrame(tick)
      if (!disposed && !frozen && videoRef.current && !videoRef.current.paused) {
        pushDue(videoRef.current.currentTime)
      }
    }

    const onPlay = () => {
      frozen = false
      manager.unfreeze()
      tick()
    }
    const onPause = () => {
      frozen = true
      manager.freeze()
      cancelAnimationFrame(raf)
    }
    const onSeeked = () => {
      // 跳转后屏幕上的弹幕瞬间失效：清空画面与待发布队列，
      // 由 rAF tick 从新位置按需重新推入（含 progress 中段补放）。
      manager.clear()
      nextIndex = 0
    }

    video.addEventListener('play', onPlay)
    video.addEventListener('playing', onPlay)
    video.addEventListener('pause', onPause)
    video.addEventListener('seeked', onSeeked)

    void loadDanmaku()
    if (!video.paused) onPlay()

    return () => {
      disposed = true
      ro.disconnect()
      cancelAnimationFrame(raf)
      video.removeEventListener('play', onPlay)
      video.removeEventListener('playing', onPlay)
      video.removeEventListener('pause', onPause)
      video.removeEventListener('seeked', onSeeked)
      manager.freeze()
      manager.clear()
      manager.unmount()
      managerRef.current = null
    }
    // search / episodeId / searchTrigger 变化时重新拉取弹幕（含媒体/开关切换）。
  }, [media, videoRef, enabled, search, episodeId, searchTrigger, onLoaded, onCandidates])

  // Live renderer knobs: opacity / area / font size without recreating the
  // engine. font size additionally rescales currently visible comments.
  useEffect(() => {
    const manager = managerRef.current
    if (!manager) return
    manager.setOpacity(opacity)
    const a = Math.min(1, Math.max(0.05, area))
    manager.setArea({ y: { start: 0, end: `${Math.round(a * 100)}%` } })
    manager.each((dm) => {
      if (dm.node) {
        dm.node.style.fontSize = `${Math.round(fontSize * dm.data.size)}px`
      }
    })
  }, [opacity, area, fontSize])

  return <div ref={holderRef} className="pointer-events-none absolute inset-0" />
}