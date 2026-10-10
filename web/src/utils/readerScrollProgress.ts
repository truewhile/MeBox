/**
 * 滚动阅读的进度保存节流。
 *
 * 只按滚动百分比的变化幅度决定是否上报，避免每帧都发保存请求。
 * 状态存在 DOM 的 dataset 上（滚动是热路径，不走 React state）。
 */

/** 触发保存的最小百分比变化（2%）。 */
export const SCROLL_SAVE_THRESHOLD_PCT = 2

/**
 * 读取上次保存的滚动百分比。
 *
 * 必须用 Number.isFinite 判断：dataset 缺失时 Number(undefined) 得到 NaN，
 * 而 NaN 与任何值比较都是 false，会导致首次滚动永远不保存（进度丢失）。
 */
export function parseLastScrollPct(raw: string | undefined): number {
  const n = Number.parseFloat(raw ?? '')
  return Number.isFinite(n) ? n : NaN
}

/**
 * 当前滚动百分比是否应该触发保存。
 *
 * @param currentPct 当前滚动百分比（0~1）
 * @param lastRaw 上次保存的原始值（dataset.last）
 */
export function shouldSaveScrollProgress(currentPct: number, lastRaw: string | undefined): boolean {
  if (!Number.isFinite(currentPct)) return false
  const last = parseLastScrollPct(lastRaw)
  if (!Number.isFinite(last)) return true
  return Math.abs(currentPct - last) * 100 >= SCROLL_SAVE_THRESHOLD_PCT
}

/** 把百分比格式化成保存用的字符串。 */
export function formatScrollPct(pct: number): string {
  return String(pct)
}
