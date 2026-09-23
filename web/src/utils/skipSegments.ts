import type { PlaybackSegment, PlaybackSegmentKind } from '../types/playback'

/** 落在源时间轴上的可跳过区间（秒）。 */
export interface SkipSegment {
  kind: PlaybackSegmentKind
  startSec: number
  endSec: number
}

/** 当前应当在浮层上提示的跳过动作。 */
export interface SkipPrompt extends SkipSegment {
  label: string
}

/** 片头/回顾统一叫「跳过片头」；片尾在有下一集时直接叫「下一集」。 */
export function skipLabel(kind: PlaybackSegmentKind, hasNextEpisode: boolean): string {
  if (kind === 'credits' || kind === 'preview') {
    return hasNextEpisode ? '下一集' : '跳过片尾'
  }
  return '跳过片头'
}

/**
 * 自动跳过后的提示文案。自动跳转很容易被当成「进度条自己动了」，所以必须告诉
 * 用户发生了什么，并给一个撤销入口。
 */
export function skippedNoticeText(kind: PlaybackSegmentKind): string {
  if (kind === 'credits' || kind === 'preview') return '已跳过片尾'
  if (kind === 'recap') return '已跳过回顾'
  return '已跳过片头'
}

/**
 * 把服务端返回的片段换算成源时间轴上的秒区间。
 *
 * end_ms === 0 表示区间延续到片尾，用媒体总时长补齐；时长未知（0 / 非有限值）
 * 时丢弃该区间——否则会算出一个永远命中的区间，浮层上的按钮就再也点不掉了。
 */
export function toSkipSegments(
  segments: PlaybackSegment[] | null | undefined,
  durationSec: number,
): SkipSegment[] {
  if (!segments || segments.length === 0) return []
  const out: SkipSegment[] = []
  for (const segment of segments) {
    const startSec = Math.max(0, segment.start_ms / 1000)
    const endSec = segment.end_ms > 0 ? segment.end_ms / 1000 : durationSec
    if (!Number.isFinite(endSec) || endSec <= startSec) continue
    out.push({ kind: segment.kind, startSec, endSec })
  }
  return out.sort((a, b) => a.startSec - b.startSec)
}

export interface ResolveActiveSkipOptions {
  hasNextEpisode?: boolean
  /** 本次播放内已经跳过 / 被用户撤销过的类型，不再提示。 */
  excludedKinds?: PlaybackSegmentKind[]
  /** 允许提前一点点出现按钮，避免恰好卡在时间轴上时迟到。 */
  toleranceSec?: number
}

/**
 * 找出当前播放位置命中的可跳过区间。同一位置有多个区间时取最早开始的那个。
 *
 * 注意 currentSec 必须是「源时间轴」上的绝对秒数：HLS 转码时 <video>.currentTime
 * 是相对转码起点的，调用方要用 streamOffset 加回来，否则转码场景整段判断都会错位。
 */
export function resolveActiveSkip(
  currentSec: number,
  segments: SkipSegment[],
  options: ResolveActiveSkipOptions = {},
): SkipPrompt | null {
  const { hasNextEpisode = false, excludedKinds = [], toleranceSec = 0.25 } = options
  if (!Number.isFinite(currentSec)) return null
  for (const segment of segments) {
    if (excludedKinds.includes(segment.kind)) continue
    if (currentSec < segment.startSec - toleranceSec) continue
    if (currentSec >= segment.endSec) continue
    return {
      kind: segment.kind,
      startSec: segment.startSec,
      endSec: segment.endSec,
      label: skipLabel(segment.kind, hasNextEpisode),
    }
  }
  return null
}
