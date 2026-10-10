import { useCallback, useEffect, useRef } from 'react'

/** 两次点击间隔在这个毫秒数以内算一次双击。 */
export const DOUBLE_TAP_MS = 280

/**
 * 双击窗口的状态。
 *
 * - `none`  空闲，没有待判定的点击
 * - `tap`   刚单击过一次，还等第二下；窗口期满仍没有第二下就触发单击
 * - `armed` 只开了双击窗口（菜单遮罩关掉菜单时用），期满什么也不触发
 */
export type DoubleTapWindow = 'none' | 'tap' | 'armed'

/** 一次动作立刻要触发的效果；null 表示只更新窗口状态、不立刻触发（留给定时器）。 */
export type DoubleTapFire = 'single' | 'double' | null

export interface DoubleTapDecision {
  window: DoubleTapWindow
  fire: DoubleTapFire
}

/**
 * 双击窗口的纯决策（不碰计时器，便于单测）：给定当前窗口状态和这次动作，
 * 得出窗口进入什么状态、要不要立刻触发回调。
 *
 * `tap` = 用户点了一下中间区域；`arm` = 只开窗口不算点击（关菜单时调用）。
 */
export function decideDoubleTap(window: DoubleTapWindow, action: 'tap' | 'arm'): DoubleTapDecision {
  if (action === 'arm') {
    // 只开窗口：已在窗口里就续期，否则新开一个 armed 窗口，永远不触发单击。
    return { window: 'armed', fire: null }
  }
  // 已经有窗口（无论是单击待定的 tap 还是刚 arm 的）→ 第二下到达，判成双击。
  if (window !== 'none') return { window: 'none', fire: 'double' }
  // 第一下：进入 tap 窗口等第二下，单击由窗口期满的定时器触发。
  return { window: 'tap', fire: null }
}

/**
 * 中间区域的「单击 / 双击」识别（手机上用来区分呼出菜单和进出全屏）。
 *
 * 中间区域原先单击就是呼出菜单，现在双击还要切全屏，两个动作落在同一块屏幕上，
 * 就得先等一小会儿看有没有第二下：第一下把窗口置为 `tap`，第二下在窗口内到达就
 * 取消单击、改判双击。**双击回调必须在第二次点击的事件里同步执行**——浏览器只认
 * 用户手势上下文里的 `requestFullscreen`，挪进定时器再调会被直接拒绝。
 *
 * 返回三个入口：
 * - `tap()`：分区点击。`enabled` 为假（桌面端，或 iOS 这种不支持元素全屏的浏览器）
 *   时不做任何延迟，单击立刻生效，端上呼出菜单的手感与改动前完全一致。
 * - `arm()`：只开一个双击窗口、不排单击。菜单打开时整屏遮罩会先吃掉那次点击
 *   （见 ReaderViewPage 的关闭遮罩），遮罩把菜单关掉的同时调它，紧跟着的第二下
 *   才能落进窗口被判成双击，而不是又当成单击把菜单重新打开。
 * - `cancel()`：丢掉待判定的点击。用户点了中间又立刻去翻页时，别让那次单击
 *   在窗口期满时把菜单弹出来。
 */
export function useDoubleTap({
  onDoubleTap,
  onSingleTap,
  enabled,
  delay = DOUBLE_TAP_MS,
}: {
  onDoubleTap: () => void
  onSingleTap: () => void
  enabled: boolean
  delay?: number
}): { tap: () => void; arm: () => void; cancel: () => void } {
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const windowRef = useRef<DoubleTapWindow>('none')
  // 回调每次渲染都可能是新闭包，用 ref 兜住，免得已排队的定时器跟着旧闭包一起失效。
  const doubleRef = useRef(onDoubleTap)
  const singleRef = useRef(onSingleTap)
  useEffect(() => {
    doubleRef.current = onDoubleTap
    singleRef.current = onSingleTap
  })

  const clear = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }, [])

  /** 取消待判定的点击并回到空闲。 */
  const cancel = useCallback(() => {
    clear()
    windowRef.current = 'none'
  }, [clear])

  /** 开一个双击窗口：期满后，tap 窗口触发单击，armed 窗口什么也不做。 */
  const startWindow = useCallback(
    (kind: DoubleTapWindow) => {
      clear()
      windowRef.current = kind
      timerRef.current = setTimeout(() => {
        timerRef.current = null
        const wasTap = windowRef.current === 'tap'
        windowRef.current = 'none'
        if (wasTap) singleRef.current()
      }, delay)
    },
    [clear, delay],
  )

  // 卸载时清掉待执行的单击，避免离开页面后还蹦一下。
  useEffect(() => cancel, [cancel])
  // 双击被关掉（如切到桌面布局）时，丢弃已排队的点击。
  useEffect(() => {
    if (!enabled) cancel()
  }, [enabled, cancel])

  const tap = useCallback(() => {
    if (!enabled) {
      singleRef.current()
      return
    }
    const decision = decideDoubleTap(windowRef.current, 'tap')
    if (decision.fire === 'double') {
      // 第二下：取消待定的单击，改判双击（同步执行，保住用户手势上下文）。
      cancel()
      doubleRef.current()
      return
    }
    startWindow(decision.window)
  }, [enabled, cancel, startWindow])

  const arm = useCallback(() => {
    if (!enabled) return
    startWindow(decideDoubleTap(windowRef.current, 'arm').window)
  }, [enabled, startWindow])

  return { tap, arm, cancel }
}
