import { useLayoutEffect } from 'react'

const SCROLL_STORAGE_PREFIX = 'mebox.scroll.'
const scrollPositions = new Map<string, number>()

function readScrollPosition(key: string): number {
  const cached = scrollPositions.get(key)
  if (cached !== undefined) return cached
  if (typeof window === 'undefined') return 0
  try {
    const value = Number(window.sessionStorage.getItem(SCROLL_STORAGE_PREFIX + key))
    if (Number.isFinite(value) && value >= 0) {
      scrollPositions.set(key, value)
      return value
    }
  } catch {
    // sessionStorage 不可用时仍可使用内存缓存。
  }
  return 0
}

function writeScrollPosition(key: string, value: number): void {
  const next = Math.max(0, Math.round(value))
  scrollPositions.set(key, next)
  if (typeof window === 'undefined') return
  try {
    window.sessionStorage.setItem(SCROLL_STORAGE_PREFIX + key, String(next))
  } catch {
    // 私密模式等场景忽略存储失败，不影响页面滚动。
  }
}

export function shouldRememberScroll(pathname: string): boolean {
  return (
    pathname === '/' ||
    pathname === '/libraries' ||
    pathname.startsWith('/library/') ||
    pathname.startsWith('/media/')
  )
}

/**
 * 路由切换时 Outlet 内容变矮，浏览器会把共享滚动容器的 scrollTop 钳低，
 * 并可能同步触发 scroll 事件。这种“假滚动”不能写入存储，否则返回时永远回到顶部。
 */
export function shouldPersistScrollSample(input: {
  current: number
  lastSaved: number
  height: number
  lastHeight: number
}): boolean {
  const { current, lastSaved, height, lastHeight } = input
  if (height + 1 < lastHeight && current < lastSaved) {
    return false
  }
  return true
}

/**
 * 记住列表页的滚动位置。页面内容会异步长高，因此恢复期间会监听内容高度，
 * 直到目标位置可达；期间用户主动滚动会立即接管，避免和恢复逻辑抢滚动条。
 */
export function useScrollMemory(pathname: string, userKey = 'anonymous'): void {
  useLayoutEffect(() => {
    const el = document.getElementById('app-main-scroll')
    if (!el) return

    if (!shouldRememberScroll(pathname)) {
      el.scrollTop = 0
      return
    }

    const key = `${userKey}:${pathname}`
    const saved = readScrollPosition(key)
    let restoring = saved > 0
    let observer: ResizeObserver | null = null
    let restoreTimer: number | null = null
    let restoreFrame = 0
    let restorePumpUntil = 0
    let lastSaved = saved
    let lastHeight = el.scrollHeight

    const stopRestore = () => {
      if (restoreFrame) {
        window.cancelAnimationFrame(restoreFrame)
        restoreFrame = 0
      }
      if (restoreTimer != null) {
        window.clearTimeout(restoreTimer)
        restoreTimer = null
      }
      observer?.disconnect()
      observer = null
    }

    const finishRestore = () => {
      if (!restoring) return
      restoring = false
      stopRestore()
      lastSaved = Math.round(el.scrollTop)
      lastHeight = el.scrollHeight
      writeScrollPosition(key, lastSaved)
    }

    const tryRestore = () => {
      if (!restoring) return
      const max = Math.max(0, el.scrollHeight - el.clientHeight)
      const target = Math.min(saved, max)
      if (target > 0 && Math.abs(el.scrollTop - target) > 1) {
        el.scrollTop = target
      }
      if (target >= saved - 1) {
        finishRestore()
      }
    }

    const pumpRestore = () => {
      if (!restoring) return
      tryRestore()
      if (restoring && performance.now() < restorePumpUntil) {
        restoreFrame = window.requestAnimationFrame(pumpRestore)
      } else {
        restoreFrame = 0
      }
    }

    const saveNow = () => {
      if (restoring) return
      const current = Math.round(el.scrollTop)
      const height = el.scrollHeight
      if (
        !shouldPersistScrollSample({
          current,
          lastSaved,
          height,
          lastHeight,
        })
      ) {
        lastHeight = height
        return
      }
      lastHeight = height
      if (current === lastSaved) return
      lastSaved = current
      writeScrollPosition(key, current)
    }

    const cancelRestore = () => {
      if (!restoring) return
      restoring = false
      stopRestore()
      lastSaved = Math.round(el.scrollTop)
      lastHeight = el.scrollHeight
      writeScrollPosition(key, lastSaved)
    }

    if (saved <= 0) {
      el.scrollTop = 0
    } else {
      tryRestore()
      if (restoring) {
        const content = el.firstElementChild
        if (typeof ResizeObserver !== 'undefined' && content instanceof HTMLElement) {
          observer = new ResizeObserver(() => tryRestore())
          observer.observe(content)
        }
        restorePumpUntil = performance.now() + 1000
        restoreFrame = window.requestAnimationFrame(pumpRestore)
        restoreTimer = window.setTimeout(finishRestore, 10000)
      }
    }

    const onPointerDown = (event: PointerEvent) => {
      if (event.target === el) {
        cancelRestore()
      }
      saveNow()
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (
        event.key === 'PageDown' ||
        event.key === 'PageUp' ||
        event.key === 'Home' ||
        event.key === 'End' ||
        event.key === 'ArrowDown' ||
        event.key === 'ArrowUp' ||
        event.key === ' '
      ) {
        cancelRestore()
      }
      saveNow()
    }

    el.addEventListener('scroll', saveNow, { passive: true })
    el.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('wheel', cancelRestore, { passive: true, capture: true })
    window.addEventListener('touchstart', cancelRestore, { passive: true, capture: true })
    window.addEventListener('keydown', onKeyDown, true)

    return () => {
      el.removeEventListener('scroll', saveNow)
      el.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('wheel', cancelRestore, true)
      window.removeEventListener('touchstart', cancelRestore, true)
      window.removeEventListener('keydown', onKeyDown, true)
      stopRestore()
      // 清理时写入最后一次有效位置，避免依赖已被钳制的 el.scrollTop。
      writeScrollPosition(key, lastSaved)
    }
  }, [pathname, userKey])
}
