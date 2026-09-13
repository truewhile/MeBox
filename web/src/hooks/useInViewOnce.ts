import { useEffect, useRef, type RefObject } from 'react'

const DEFAULT_ROOT_MARGIN = '480px 0px'

/**
 * 元素首次进入滚动容器附近时触发一次。用于媒体库预览的按需加载。
 */
export function useInViewOnce<T extends Element>(
  onVisible: () => void,
  rootMargin = DEFAULT_ROOT_MARGIN,
): RefObject<T> {
  const ref = useRef<T>(null)
  const callbackRef = useRef(onVisible)

  useEffect(() => {
    callbackRef.current = onVisible
  }, [onVisible])

  useEffect(() => {
    const element = ref.current
    if (!element) return

    if (typeof IntersectionObserver === 'undefined') {
      callbackRef.current()
      return
    }

    const root = document.getElementById('app-main-scroll')
    let fired = false
    const observer = new IntersectionObserver(
      (entries) => {
        if (fired || !entries.some((entry) => entry.isIntersecting)) return
        fired = true
        observer.disconnect()
        callbackRef.current()
      },
      { root, rootMargin, threshold: 0.01 },
    )
    observer.observe(element)
    return () => observer.disconnect()
  }, [rootMargin])

  return ref
}
