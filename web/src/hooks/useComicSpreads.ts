import { useEffect, useMemo, useState } from 'react'

import {
  buildSpreadIndexOf,
  buildSpreads,
  hasImageSize,
  loadImageSize,
  type ComicSpread,
} from '../utils/comicSpread'

// 漫画双页铺开的分组 + 尺寸探测。
//
// 分组结果依赖每张图是否「横跨两页」，而这要等图片尺寸探测出来才能判断，
// 于是探测每完成一张就 bump 一次版本号、重新分组。为避免把整章图片都拉一遍，
// 只探测当前页附近的一小段窗口：向前看 8 张，翻到下一屏时它的尺寸已经就绪，
// 不会出现「先按竖版配好、渲染完再重排」的跳动。

/** 当前页往后探测几张。 */
const PROBE_AHEAD = 8
/** 当前页往前回看几张（滑块往回拖时补上断档）。 */
const PROBE_BEHIND = 1

export interface ComicSpreads {
  /** 每屏要显示的图片序号。 */
  spreads: ComicSpread[]
  /** 图片序号 → 所在屏序号。 */
  spreadIndexOf: number[]
}

/**
 * @param srcs 整章图片地址。
 * @param enabled 只在真的会用到双页时探测，单页/滚动模式不额外发请求。
 * @param center 当前图片序号，决定探测窗口。
 */
export function useComicSpreads(srcs: string[], enabled: boolean, center: number): ComicSpreads {
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    if (!enabled || srcs.length === 0) return
    let cancelled = false
    const from = Math.max(0, center - PROBE_BEHIND)
    const to = Math.min(srcs.length - 1, center + PROBE_AHEAD)
    const targets: string[] = []
    for (let i = from; i <= to; i += 1) {
      const src = srcs[i]
      if (src && !hasImageSize(src)) targets.push(src)
    }
    if (targets.length === 0) return
    for (const src of targets) {
      loadImageSize(src)
        .then(() => {
          if (!cancelled) setRevision((v) => v + 1)
        })
        .catch(() => undefined)
    }
    return () => {
      cancelled = true
    }
  }, [srcs, enabled, center])

  const spreads = useMemo(() => {
    // revision 参与依赖：尺寸探测每完成一张就重新分组，宽图才能正确独占一屏
    void revision
    return buildSpreads(srcs)
  }, [srcs, revision])

  const spreadIndexOf = useMemo(() => buildSpreadIndexOf(spreads, srcs.length), [spreads, srcs.length])

  return { spreads, spreadIndexOf }
}
