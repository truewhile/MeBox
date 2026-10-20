import { useEffect, useRef, type RefObject } from 'react'

/**
 * 滚轮平滑滚动：把离散的滚轮格数累加成一个「目标位置」，
 * 再让 scrollTop 每帧向它逼近，于是滚动像手机上下滑动一样连续、带减速惯性，
 * 而不是浏览器的整格跳变。
 *
 * 参考帧间隔（60Hz）下的逼近比例：越大越跟手，越小越飘。
 */
const EASE_PER_FRAME = 0.2
/** 每帧的基准毫秒数。按真实帧间隔换算，120Hz 屏上速度才不会翻倍。 */
const FRAME_MS = 1000 / 60
/** 与目标相差小于这个像素直接落位，省掉最后一帧帧逼近。 */
const EPSILON = 0.5
/** 单帧最大推进间隔，避免切标签页回来后一步跳很远。 */
const MAX_FRAME_MS = 64

/**
 * 给一个纵向滚动容器接上「类手机滑动」的滚轮滚动。
 *
 * - 只用滚轮事件驱动，触摸/拖动滚动条时立刻交还控制权，不和手指打架。
 * - `ctrl/cmd + 滚轮`（浏览器缩放）不接管。
 * - 容器不可滚动时不做 `preventDefault`。
 *
 * @param ref 滚动容器（`overflow-y: auto`）。
 * @param enabled 是否接管；翻页模式等由别处处理滚轮的场景传 `false`。
 */
export function useSmoothWheelScroll(ref: RefObject<HTMLElement>, enabled: boolean): void {
  const targetRef = useRef<number | null>(null)
  const rafRef = useRef<number | null>(null)
  const lastFrameRef = useRef(0)

  useEffect(() => {
    const el = ref.current
    if (!el || !enabled) return

    const stop = () => {
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
      targetRef.current = null
      lastFrameRef.current = 0
    }

    const step = (now: number) => {
      rafRef.current = null
      const target = targetRef.current
      const node = ref.current
      if (target === null || !node) return

      const dt = lastFrameRef.current === 0 ? FRAME_MS : Math.min(MAX_FRAME_MS, now - lastFrameRef.current)
      lastFrameRef.current = now

      const diff = target - node.scrollTop
      if (Math.abs(diff) < EPSILON) {
        node.scrollTop = target
        targetRef.current = null
        lastFrameRef.current = 0
        return
      }

      const before = node.scrollTop
      node.scrollTop = before + diff * (1 - Math.pow(1 - EASE_PER_FRAME, dt / FRAME_MS))
      // 到了滚动边界（或内容变短被浏览器夹住）时 scrollTop 不动了，
      // 再继续逼近就是 rAF 空转，直接收手。
      if (node.scrollTop === before) {
        targetRef.current = null
        lastFrameRef.current = 0
        return
      }
      rafRef.current = requestAnimationFrame(step)
    }

    const onWheel = (e: WheelEvent) => {
      // ctrl/cmd+滚轮是浏览器缩放，别抢
      if (e.ctrlKey || e.metaKey || e.deltaY === 0) return
      const max = el.scrollHeight - el.clientHeight
      if (max <= 0) return
      e.preventDefault()
      // deltaMode: 0=像素 1=行 2=页，统一折算成像素
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? el.clientHeight : 1
      const base = targetRef.current ?? el.scrollTop
      targetRef.current = Math.min(max, Math.max(0, base + e.deltaY * unit))
      if (rafRef.current === null) {
        lastFrameRef.current = 0
        rafRef.current = requestAnimationFrame(step)
      }
    }

    // 手指按下（移动端滑动、拖滚动条、点分区）立刻停掉动画，把控制权交还用户
    const onTakeover = () => stop()

    el.addEventListener('wheel', onWheel, { passive: false })
    el.addEventListener('pointerdown', onTakeover)
    el.addEventListener('touchstart', onTakeover, { passive: true })
    return () => {
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('pointerdown', onTakeover)
      el.removeEventListener('touchstart', onTakeover)
      stop()
    }
  }, [ref, enabled])
}
