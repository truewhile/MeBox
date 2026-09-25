/** STRM/115 直连跳转：等 seekable 覆盖目标后再写 currentTime，避免被钳回 0。 */

export const DIRECT_SEEK_WAIT_MS = 10_000
export const DIRECT_SEEK_MARGIN_SEC = 0.35

type SeekableLike = {
  length: number
  start: (index: number) => number
  end: (index: number) => number
}

/** 目标时间是否落在任一 seekable 区间内（末端留一点边距）。 */
export function timeInSeekableRanges(
  seekable: SeekableLike | null | undefined,
  time: number,
  margin = DIRECT_SEEK_MARGIN_SEC,
): boolean {
  if (!seekable || seekable.length <= 0) return false
  if (!Number.isFinite(time) || time < 0) return false
  for (let i = 0; i < seekable.length; i += 1) {
    try {
      const start = seekable.start(i)
      const end = seekable.end(i)
      if (!Number.isFinite(start) || !Number.isFinite(end)) continue
      if (time + margin >= start && time <= end + margin) return true
    } catch {
      // TimeRanges 在部分浏览器上可能抛 InvalidStateError，跳过该段。
    }
  }
  return false
}

/** seekable 已覆盖的最右端（秒）；没有可用区间时返回 0。 */
export function seekableEndSec(seekable: SeekableLike | null | undefined): number {
  if (!seekable || seekable.length <= 0) return 0
  let max = 0
  for (let i = 0; i < seekable.length; i += 1) {
    try {
      const end = seekable.end(i)
      if (Number.isFinite(end) && end > max) max = end
    } catch {
      // ignore
    }
  }
  return max
}

/**
 * 直连是否可以立刻跳到 target。
 * - seekable 已覆盖 → 可以
 * - 目标接近 0 → 可以（不需要等）
 * - 否则应等待 progress / durationchange
 */
export function canSeekDirectNow(
  seekable: SeekableLike | null | undefined,
  time: number,
): boolean {
  if (!Number.isFinite(time) || time <= 1) return true
  return timeInSeekableRanges(seekable, time)
}
