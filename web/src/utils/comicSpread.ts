// 漫画「双页铺开」的纯逻辑与图片尺寸探测。
//
// 桌面端一屏能并排放下两页，漫画按「左右两页」成对观看才接近纸质阅读体验
// （尤其日漫的跨页）。麻烦在于章节里常混着「本来就横跨两页」的宽图（跨页扉页、
// 彩页）：这类图必须独占一屏，否则会被硬塞进一对里，从它开始整章都错位。
//
// 所以分组规则与 Tachiyomi 的 dual page 一致：宽度明显大于高度的图独占一组，
// 其余连续的竖版页按顺序两两成对。判断需要原始宽高，而列表接口只给 URL，
// 这里就自己探测并缓存（<img> 渲染时也会顺带回填，能省掉一次请求）。

/** 图片原始像素尺寸。 */
export interface ImageSize {
  width: number
  height: number
}

/** 宽高比达到这个值即视为「跨页宽图」。普通页约 0.6～0.7，跨页图约 1.3～1.5。 */
const WIDE_RATIO = 1.1

/** 已探测到的尺寸，按 URL 缓存（null = 探测失败，当竖版处理），跨章节复用。 */
const sizeCache = new Map<string, ImageSize | null>()
/** 正在探测的图片，避免同一 URL 并发发起多次请求。 */
const inflight = new Map<string, Promise<ImageSize | null>>()

/** 渲染中的 <img> onLoad 顺带回填尺寸，省掉一次探测请求。 */
export function rememberImageSize(src: string, width: number, height: number): void {
  if (!src || !width || !height) return
  sizeCache.set(src, { width, height })
}

/** 该 URL 是否已经探测过（成功的记尺寸，失败的记 null）。 */
export function hasImageSize(src: string): boolean {
  return sizeCache.has(src)
}

/** 探测一张图片的原始尺寸；失败（404 / 被拦 / 解码失败）返回 null。 */
export function loadImageSize(src: string): Promise<ImageSize | null> {
  const cached = sizeCache.get(src)
  if (cached !== undefined) return Promise.resolve(cached)
  const running = inflight.get(src)
  if (running) return running
  const task = new Promise<ImageSize | null>((resolve) => {
    const img = new Image()
    const settle = (size: ImageSize | null) => {
      sizeCache.set(src, size)
      inflight.delete(src)
      resolve(size)
    }
    img.onload = () =>
      settle(
        img.naturalWidth > 0 && img.naturalHeight > 0
          ? { width: img.naturalWidth, height: img.naturalHeight }
          : null,
      )
    img.onerror = () => settle(null)
    img.src = src
  })
  inflight.set(src, task)
  return task
}

/** 该图是否横跨两页（宽明显大于高）。尺寸未知时按竖版处理，宁可先配对。 */
export function isWideImage(src: string): boolean {
  const size = sizeCache.get(src)
  if (!size) return false
  return size.width / size.height >= WIDE_RATIO
}

/** 一「屏」要显示的图片序号：一张=独占的宽图/单页，两张=左右并排的一对。 */
export type ComicSpread = number[]

/**
 * 把整章图片按双页铺开分组：连续的竖版页两两成对，宽图独占一组并打断前后配对。
 * 尺寸还没探测出来的图先按竖版参与配对，探测完成后再重新分组（见 useComicSpreads）。
 */
export function buildSpreads(srcs: string[]): ComicSpread[] {
  const spreads: ComicSpread[] = []
  let i = 0
  while (i < srcs.length) {
    if (isWideImage(srcs[i])) {
      spreads.push([i])
      i += 1
      continue
    }
    // 收集一段连续的竖版页，两两成对；落单的最后一张自己一屏（常见于章尾）
    const run: number[] = []
    while (i < srcs.length && !isWideImage(srcs[i])) {
      run.push(i)
      i += 1
    }
    for (let k = 0; k < run.length; k += 2) spreads.push(run.slice(k, k + 2))
  }
  return spreads
}

/**
 * 图片序号 → 所在屏序号。进度是按图片序号记的，翻页要换算成「当前在第几屏」，
 * 并且不管展开/收起双页都稳定（只由图片序号推出来，不额外存一屏序号）。
 */
export function buildSpreadIndexOf(spreads: ComicSpread[], total: number): number[] {
  const map = new Array<number>(Math.max(0, total)).fill(0)
  spreads.forEach((spread, si) => {
    for (const idx of spread) {
      if (idx >= 0 && idx < map.length) map[idx] = si
    }
  })
  return map
}
