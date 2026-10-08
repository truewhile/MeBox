import { useCallback, useRef, type TouchEvent as ReactTouchEvent } from 'react'

// 横向滑动手势：给「左右翻页」的阅读区补上手机上的滑动手势。
// 原来只能点左 30% / 右 30% 分区翻页，手指横滑没有任何反应，翻页很别扭。
//
// 判定规则（和纵向滚动共存的关键）：
// - 位移要够长（minDistance），短距离当点击处理，交给下面的分区点击；
// - 横向位移要明显大于纵向位移（dominance 倍），否则算纵向滑动/滚动，不翻页；
// - 手势时长不能太长（maxDuration），慢慢拖过去不算一次滑动。
//
// 一次滑动之后浏览器仍可能补一个 click（落在原来的分区按钮上），
// 于是会「滑一页又点一页」翻两页。用 consumeSwipe() 让点击回调先问一句：
// 这次 click 是不是刚被滑动吃掉，是则跳过。每次 touchstart 会重置这个标记，
// 所以真实滑动不产生 click 时也不会误吞下一次点击。

interface HorizontalSwipeOptions {
  /** 从左往右滑（手指向右移）。 */
  onSwipeRight: () => void
  /** 从右往左滑（手指向左移）。 */
  onSwipeLeft: () => void
  /** 触发翻页所需的最小水平位移（px）。 */
  minDistance?: number
  /** 水平位移需达到垂直位移的这个倍数才算横滑，避免和纵向滚动打架。 */
  dominance?: number
  /** 手势超过这个时长不算滑动（毫秒）。 */
  maxDuration?: number
}

interface HorizontalSwipeHandlers {
  onTouchStart: (e: ReactTouchEvent) => void
  onTouchEnd: (e: ReactTouchEvent) => void
  onTouchCancel: () => void
  /** 点击回调里先调用；返回 true 表示这次点击已被滑动消费，应跳过默认动作。 */
  consumeSwipe: () => boolean
}

export function useHorizontalSwipe({
  onSwipeRight,
  onSwipeLeft,
  minDistance = 45,
  dominance = 1.2,
  maxDuration = 800,
}: HorizontalSwipeOptions): HorizontalSwipeHandlers {
  const startRef = useRef<{ x: number; y: number; t: number } | null>(null)
  const consumedRef = useRef(false)

  const onTouchStart = useCallback((e: ReactTouchEvent) => {
    const t = e.touches[0]
    if (!t) return
    startRef.current = { x: t.clientX, y: t.clientY, t: Date.now() }
    consumedRef.current = false
  }, [])

  const onTouchEnd = useCallback(
    (e: ReactTouchEvent) => {
      const start = startRef.current
      startRef.current = null
      const t = e.changedTouches[0]
      if (!start || !t) return

      const dx = t.clientX - start.x
      const dy = t.clientY - start.y
      if (Date.now() - start.t > maxDuration) return
      if (Math.abs(dx) < minDistance) return
      if (Math.abs(dx) < Math.abs(dy) * dominance) return

      consumedRef.current = true
      if (dx > 0) onSwipeRight()
      else onSwipeLeft()
    },
    [onSwipeRight, onSwipeLeft, minDistance, dominance, maxDuration],
  )

  const onTouchCancel = useCallback(() => {
    startRef.current = null
  }, [])

  const consumeSwipe = useCallback(() => {
    if (!consumedRef.current) return false
    consumedRef.current = false
    return true
  }, [])

  return { onTouchStart, onTouchEnd, onTouchCancel, consumeSwipe }
}
