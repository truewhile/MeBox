import { useCallback, useEffect, useState } from 'react'

// 列表分页位置（媒体库入口当前页、媒体库已加载页数、货架解锁数量）需要在
// 进入详情页再返回后依然生效，所以用 sessionStorage 记录，并在内存里留一份
// 兜底缓存；同一标签页内刷新也仍然记得。
const LIST_POSITION_PREFIX = 'mebox.listpos.'
const positions = new Map<string, number>()

/**
 * 详情页返回时最多自动补拉的分页数量：滚动到很深的用户重新进入列表时
 * 依次补拉这些页，剩下的继续由底部哨兵按需加载，避免一次打太多请求。
 */
export const MAX_RESTORE_PAGES = 8

/** 把存储里的任意值收敛成 >= 1 的整数分页位置。 */
export function normalizeListPosition(
  value: unknown,
  fallback = 1,
  max = Number.MAX_SAFE_INTEGER,
): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(numeric)) return fallback
  const rounded = Math.floor(numeric)
  if (rounded < 1) return fallback
  return Math.min(rounded, max)
}

export function readListPosition(key: string, fallback = 1, max = Number.MAX_SAFE_INTEGER): number {
  const cached = positions.get(key)
  if (cached !== undefined) return Math.min(cached, max)
  if (typeof window === 'undefined') return fallback
  try {
    const raw = window.sessionStorage.getItem(LIST_POSITION_PREFIX + key)
    if (raw === null) return fallback
    const value = normalizeListPosition(raw, fallback, max)
    positions.set(key, value)
    return value
  } catch {
    return fallback
  }
}

export function writeListPosition(key: string, value: number): void {
  const next = normalizeListPosition(value)
  positions.set(key, next)
  if (typeof window === 'undefined') return
  try {
    window.sessionStorage.setItem(LIST_POSITION_PREFIX + key, String(next))
  } catch {
    // 私密模式等场景忽略存储失败，不影响分页。
  }
}

export function forgetListPosition(key: string): void {
  positions.delete(key)
  if (typeof window === 'undefined') return
  try {
    window.sessionStorage.removeItem(LIST_POSITION_PREFIX + key)
  } catch {
    // 同上，存储不可用时无需处理。
  }
}

/**
 * 记住列表当前分页位置。key 需要在一份挂载生命周期内保持稳定：路由切换会
 * 让列表重新挂载，因此每次挂载都能直接读到上次记录的位置（不会先闪回第一页）。
 */
export function useRememberedListPosition(
  key: string,
  fallback = 1,
  max = Number.MAX_SAFE_INTEGER,
): [number, (next: number | ((prev: number) => number)) => void] {
  const [position, setPosition] = useState(() => readListPosition(key, fallback, max))

  useEffect(() => {
    writeListPosition(key, position)
  }, [key, position])

  const remember = useCallback((next: number | ((prev: number) => number)) => {
    setPosition((prev) => {
      const value = typeof next === 'function' ? next(prev) : next
      return normalizeListPosition(value, prev)
    })
  }, [])

  return [position, remember]
}
