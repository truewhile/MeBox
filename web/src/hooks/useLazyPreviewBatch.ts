import { useCallback, useEffect, useRef } from 'react'

/**
 * 合并短时间内进入视口的预览请求，避免每个卡片各发一次 HTTP 请求。
 * 回调放在 ref 中，调用方每次渲染传新的闭包也不会重建定时器。
 */
export function useLazyPreviewBatch(
  onNeedPreviews?: (ids: string[]) => void,
  delayMs = 48,
) {
  const pendingRef = useRef<Set<string>>(new Set())
  const timerRef = useRef<number | null>(null)
  const callbackRef = useRef(onNeedPreviews)

  useEffect(() => {
    callbackRef.current = onNeedPreviews
  }, [onNeedPreviews])

  useEffect(() => {
    return () => {
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
      }
    }
  }, [])

  return useCallback((id: string) => {
    if (!id || !callbackRef.current) return
    pendingRef.current.add(id)
    if (timerRef.current !== null) return
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null
      const ids = Array.from(pendingRef.current)
      pendingRef.current.clear()
      if (ids.length > 0) {
        callbackRef.current?.(ids)
      }
    }, delayMs)
  }, [delayMs])
}
