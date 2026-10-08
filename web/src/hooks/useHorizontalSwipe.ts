import { useCallback, useRef, type TouchEvent as ReactTouchEvent } from 'react'

// 横向滑动手势：给「左右翻页」的阅读区补上手机上的滑动手势。
// 原来只能点左 30% / 右 30% 分区翻页，手指横滑没有任何反应，翻页很别扭。
//
// 判定规则（和纵向滚动共存的关键）：
// - 方向要先判定出来：位移超过 slop 后，横向位移要明显大于纵向位移（dominance 倍）
//   才算横滑，否则算纵向滑动/滚动，本次手势彻底放弃（连 click 也不吞）；
// - 没走跟手（点按、或没传 onDrag）时，仍按 minDistance + maxDuration 判定一次滑动，
//   短距离当点击处理，交给下面的分区点击。
//
// 跟手拖拽：传了 onDrag 之后，手指横向移动会实时回调位移，松手再回调 onDragEnd，
// 由宿主把内容平移到位（正文列 / 漫画舞台）并决定翻页还是回弹。滑动途中内容就跟着
// 手指走，不再等到松手才动一下。跟手手势一律按「拖动」处理，不再走 maxDuration 判定
// （慢慢拖过去也是拖动）。
//
// 一次滑动之后浏览器仍可能补一个 click（落在原来的分区按钮上），
// 于是会「滑一页又点一页」翻两页。用 consumeSwipe() 让点击回调先问一句：
// 这次 click 是不是刚被滑动吃掉，是则跳过。每次 touchstart 会重置这个标记，
// 所以真实滑动不产生 click 时也不会误吞下一次点击。

/** 判定手势方向的位移阈值（px）：比这还小就还在点按的抖动范围内。 */
const DIRECTION_SLOP = 12

interface HorizontalSwipeOptions {
  /** 从左往右滑（手指向右移）。 */
  onSwipeRight: () => void
  /** 从右往左滑（手指向左移）。 */
  onSwipeLeft: () => void
  /** 触发翻页所需的最小水平位移（px）。 */
  minDistance?: number
  /** 水平位移需达到垂直位移的这个倍数才算横滑，避免和纵向滚动打架。 */
  dominance?: number
  /** 手势超过这个时长不算滑动（毫秒）。只对非跟手的滑动判定生效。 */
  maxDuration?: number
  /**
   * 跟手拖拽开始（方向判定通过、第一次 onDrag 之前）调一次。
   * 宿主在这里记下跟手层当前的实际位移：上一次翻页的归位动画可能还没滑完，
   * 新手势要接着那点残余位移继续，否则中途接管会把残余一笔抹掉，画面跳一格。
   */
  onDragStart?: () => void
  /** 跟手拖拽中的位移（px，向右为正）。传了才启用跟手。 */
  onDrag?: (dx: number) => void
  /** 跟手松手：宿主在这里回弹或翻页（随后浏览器补发的 click 会被吞掉）。 */
  onDragEnd?: (dx: number) => void
}

interface HorizontalSwipeHandlers {
  onTouchStart: (e: ReactTouchEvent) => void
  onTouchMove: (e: ReactTouchEvent) => void
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
  onDragStart,
  onDrag,
  onDragEnd,
}: HorizontalSwipeOptions): HorizontalSwipeHandlers {
  const startRef = useRef<{ x: number; y: number; t: number } | null>(null)
  const consumedRef = useRef(false)
  /** 本次手势是否已判定为横滑并在跟手。 */
  const draggingRef = useRef(false)
  const lastDxRef = useRef(0)

  const onTouchStart = useCallback((e: ReactTouchEvent) => {
    const t = e.touches[0]
    if (!t) return
    startRef.current = { x: t.clientX, y: t.clientY, t: Date.now() }
    consumedRef.current = false
    draggingRef.current = false
    lastDxRef.current = 0
  }, [])

  const onTouchMove = useCallback(
    (e: ReactTouchEvent) => {
      const start = startRef.current
      const t = e.touches[0]
      if (!start || !t) return
      const dx = t.clientX - start.x
      const dy = t.clientY - start.y
      if (!draggingRef.current) {
        if (!onDrag) return
        if (Math.abs(dx) < DIRECTION_SLOP && Math.abs(dy) < DIRECTION_SLOP) return
        // 纵向占优：这是滚动，不是翻页。丢掉起点，之后不再判定滑动也不吞 click。
        if (Math.abs(dx) < Math.abs(dy) * dominance) {
          startRef.current = null
          return
        }
        draggingRef.current = true
        onDragStart?.()
      }
      lastDxRef.current = dx
      onDrag?.(dx)
    },
    [onDragStart, onDrag, dominance],
  )

  const onTouchEnd = useCallback(
    (e: ReactTouchEvent) => {
      const start = startRef.current
      startRef.current = null
      if (draggingRef.current) {
        draggingRef.current = false
        // 跟手手势一律吞掉随之而来的 click：翻页已经在 onDragEnd 里决定了，
        // 不吞的话短距离拖动会「回弹 + 再翻一页」。
        consumedRef.current = true
        const t = e.changedTouches[0]
        // 用松手这一刻的位置而不是最后一次 touchmove：手指最后那几像素也要算进去。
        const dx = start && t ? t.clientX - start.x : lastDxRef.current
        onDragEnd?.(dx)
        return
      }
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
    [onSwipeRight, onSwipeLeft, minDistance, dominance, maxDuration, onDragEnd],
  )

  const onTouchCancel = useCallback(() => {
    startRef.current = null
    if (!draggingRef.current) return
    draggingRef.current = false
    // 手势被系统打断（来电、边缘返回手势）：按「没翻页」处理，让内容回弹。
    // 传 0 而不是最后的位置——打断不等于用户想翻页，别顺手翻过去。
    onDragEnd?.(0)
  }, [onDragEnd])

  const consumeSwipe = useCallback(() => {
    if (!consumedRef.current) return false
    consumedRef.current = false
    return true
  }, [])

  return { onTouchStart, onTouchMove, onTouchEnd, onTouchCancel, consumeSwipe }
}
