import { useLayoutEffect } from 'react'

const SCROLL_STORAGE_PREFIX = 'mebox.scroll.'
const scrollPositions = new Map<string, number>()

/**
 * 用户滚动意图的有效期：滚轮 / 触摸 / 翻页键之后这段时间内的 scroll 事件才算
 * “用户自己滚出来的位置”。连续滚动（含惯性）期间每个 scroll 事件都会续期，
 * 所以正常滑动不会中途过期，而恢复回填 / 虚拟列表量高这类程序化位移会被排除。
 */
const USER_SCROLL_INTENT_TTL_MS = 800

/** 平滑滚动（滚轮/触摸/方向键）单次采样允许的最大“向上跳”距离。 */
function maxSmoothJumpUp(viewportHeight: number): number {
  return Math.max(viewportHeight * 2, 1200)
}

/** 会滚动容器的按键。 */
export function isScrollIntentKey(key: string): boolean {
  return (
    key === 'PageDown' ||
    key === 'PageUp' ||
    key === 'Home' ||
    key === 'End' ||
    key === 'ArrowDown' ||
    key === 'ArrowUp' ||
    key === ' '
  )
}

/** 一次按键可能直接跳很远（首页/末页/翻页），这些不算异常向上跳。 */
export function isJumpIntentKey(key: string): boolean {
  return key === 'PageDown' || key === 'PageUp' || key === 'Home' || key === 'End'
}

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
    pathname === '/search' ||
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
 * 判断一次滚动采样是不是“用户真正选中的位置”。
 *
 * 内容还在加载（容器根本滚不动）或被钳在“变矮内容”的底部时，scrollTop 是浏览器
 * 钳制出来的值：恢复途中任何一次滚轮 / 触摸 / 按键都会读到这个值，写回存储就会把
 * 记忆位置抹成 0（表现为返回媒体库时永远停在顶部，且之后再也不会记录）。
 */
export function shouldPersistClampedSample(input: {
  current: number
  maxScroll: number
  saved: number
}): boolean {
  const { current, maxScroll, saved } = input
  if (maxScroll <= 0) {
    return false
  }
  if (current >= maxScroll && current < saved) {
    return false
  }
  return true
}

/**
 * 恢复完成后的看守时长：列表在这一小段时间里仍可能重排（分页补页让普通网格
 * 切换成虚拟列表、海报量高……），内容高度瞬间被压塌时浏览器会把 scrollTop
 * 夹小，表现就是“返回列表后跳到顶部”。这段时间内没有用户滚动意图就把记忆
 * 位置贴回去。
 */
const RESTORE_GUARD_MS = 3000

/**
 * 看守期里哪些 scrollTop 跌落要贴回记忆位置。
 *
 * 只处理“内容还够长、用户没有滚动意图、位置却掉到记忆位置上方”的跌落；
 * 用户自己滚上去（有滚动意图）不干预，内容还没长回原来的高度也不强行回填。
 */
export function shouldReapplyGuardedScroll(input: {
  current: number
  target: number
  maxScroll: number
  userDriven: boolean
  now: number
  guardUntil: number
}): boolean {
  const { current, target, maxScroll, userDriven, now, guardUntil } = input
  if (now > guardUntil) return false
  if (userDriven) return false
  if (maxScroll <= 0 || target <= 0) return false
  return current < target - 1
}

/**
 * 记住列表页的滚动位置。页面内容会异步长高，因此恢复期间会监听内容高度，
 * 直到目标位置可达；期间用户主动滚动会立即接管，避免和恢复逻辑抢滚动条。
 *
 * 恢复还没完成时，容器里的 scrollTop 往往是浏览器钳制出来的值（内容还在加载
 * 占位，或被钳在变矮内容的底部）。这种采样既不写回存储，也不当成用户“接管”，
 * 否则一次误触的滚轮 / 触摸就会把记忆位置清成 0。
 *
 * 只有带“用户滚动意图”（滚轮 / 触摸 / 翻页键 / 拖滚动条）的 scroll 事件才会写
 * 存储：恢复回填、虚拟列表挂载量高、浏览器钳制造成的位移都不算用户选择的位置。
 *
 * routeKey 允许包含 query：媒体库详情页的剧集面板（`/library/:id?series=...`）
 * 与网格视图是两块不同内容，各自记一份位置，互不覆盖。
 */
export function useScrollMemory(routeKey: string, userKey = 'anonymous'): void {
  useLayoutEffect(() => {
    const el = document.getElementById('app-main-scroll')
    if (!el) return

    const pathname = routeKey.split('?')[0]
    if (!shouldRememberScroll(pathname)) {
      el.scrollTop = 0
      return
    }

    const key = `${userKey}:${routeKey}`
    const saved = readScrollPosition(key)
    let restoring = saved > 0
    let observer: ResizeObserver | null = null
    let restoreTimer: number | null = null
    let restoreFrame = 0
    let restorePumpUntil = 0
    // 恢复完成后的看守截止时间：见 RESTORE_GUARD_MS。
    let guardUntil = 0
    // 看守期里被“用户正在滚动”挡下的那次回填，等滚动意图过期后补做一次。
    let guardRetryTimer: number | null = null
    let lastSaved = saved
    let lastHeight = el.scrollHeight
    // 内容容器（`<main>` 里那层宽度约束 div）：分页补页、列表切换渲染方式都会
    // 改变它的高度，恢复回填与看守都靠它驱动。
    const content = el.firstElementChild instanceof HTMLElement ? el.firstElementChild : null
    // 最近一次“用户主动滚动”的时间戳与类型（滚轮 / 触摸 / 翻页键 / 拖滚动条）。
    // 只有用户自己滚出来的位置才写回存储：恢复逻辑、虚拟列表挂载量高、浏览器
    // 钳制造成的 scrollTop 变化都不代表用户想要的位置，写进去就会把记忆抹掉。
    let userScrollUntil = 0
    let userScrollAllowsJump = false
    const markUserScroll = (allowsJump = false) => {
      const now = performance.now()
      // 上一段意图已过期：新的一段从“只允许小幅向上跳”开始。
      if (userScrollUntil <= now) {
        userScrollAllowsJump = false
      }
      userScrollUntil = now + USER_SCROLL_INTENT_TTL_MS
      if (allowsJump) {
        userScrollAllowsJump = true
      }
    }
    const userScrolling = () => userScrollUntil > performance.now()

    const currentMaxScroll = () => Math.max(0, el.scrollHeight - el.clientHeight)

    // 只有“用户真正能滚到”的位置才算有效采样：内容还在加载占位（滚不动）或
    // 被钳在变矮内容的底部时，scrollTop 都是浏览器钳制出来的值。
    const sampleIsTrustworthy = (current: number) =>
      shouldPersistClampedSample({ current, maxScroll: currentMaxScroll(), saved })

    const adoptScrollPosition = (current: number) => {
      lastSaved = current
      lastHeight = el.scrollHeight
      writeScrollPosition(key, current)
    }

    const stopRestore = () => {
      if (restoreFrame) {
        window.cancelAnimationFrame(restoreFrame)
        restoreFrame = 0
      }
      if (restoreTimer != null) {
        window.clearTimeout(restoreTimer)
        restoreTimer = null
      }
      if (guardRetryTimer != null) {
        window.clearTimeout(guardRetryTimer)
        guardRetryTimer = null
      }
      observer?.disconnect()
      observer = null
      guardUntil = 0
    }

    /** 恢复完成后继续看守一小段时间，见 RESTORE_GUARD_MS。 */
    const startGuard = (target: number) => {
      if (target <= 0) return
      guardUntil = performance.now() + RESTORE_GUARD_MS
      if (restoreFrame) {
        window.cancelAnimationFrame(restoreFrame)
        restoreFrame = 0
      }
      if (restoreTimer != null) window.clearTimeout(restoreTimer)
      restoreTimer = window.setTimeout(() => {
        restoreTimer = null
        stopRestore()
      }, RESTORE_GUARD_MS)
    }

    /**
     * 看守期里的塌陷回填：列表重排（普通网格换成虚拟列表、海报重新量高）会让
     * 内容瞬间变矮，浏览器随之把 scrollTop 夹小，表现就是“返回列表后跳到顶部”。
     * 用户没有滚动意图时把记忆位置贴回去。
     */
    const reapplyGuardedScroll = () => {
      if (restoring) return
      const max = currentMaxScroll()
      const target = Math.min(lastSaved, max)
      if (
        shouldReapplyGuardedScroll({
          current: el.scrollTop,
          target,
          maxScroll: max,
          userDriven: userScrolling(),
          now: performance.now(),
          guardUntil,
        })
      ) {
        el.scrollTop = target
        return
      }
      // 这次塌陷正好撞上用户还在滚动：等滚动意图过期后再补做一次，别把它漏掉
      // （否则用户滚完停手，列表就停在被夹出来的顶部不动了）。
      if (userScrolling() && performance.now() <= guardUntil && guardRetryTimer == null) {
        guardRetryTimer = window.setTimeout(() => {
          guardRetryTimer = null
          reapplyGuardedScroll()
        }, USER_SCROLL_INTENT_TTL_MS)
      }
    }

    const onContentSizeChange = () => {
      if (restoring) {
        tryRestore()
        return
      }
      reapplyGuardedScroll()
    }

    const finishRestore = () => {
      if (!restoring) return
      restoring = false
      const current = Math.round(el.scrollTop)
      // 恢复了半天位置却掉到记忆位置上方很远，而且不是用户自己滚上去的：
      // 这是浏览器把 scrollTop 夹出来的值（内容被压塌、还在加载占位）。
      // 记忆值必须保留，否则下次进来就真的从顶部开始了。
      const droppedFar =
        saved - current > maxSmoothJumpUp(el.clientHeight) && !userScrolling()
      if (droppedFar || !sampleIsTrustworthy(current)) {
        stopRestore()
        return
      }
      adoptScrollPosition(current)
      startGuard(current)
    }

    const tryRestore = () => {
      if (!restoring) return
      // 用户正在滚动：本帧不要回填，把控制权交给他（交给 saveNow 的接管分支）。
      if (userScrolling()) return
      const max = currentMaxScroll()
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
      const current = Math.round(el.scrollTop)
      const userDriven = userScrolling()
      // 惯性/连续滚动期间持续续期，避免长距离滑动中途被判定为“非用户滚动”。
      if (userDriven) markUserScroll(userScrollAllowsJump)
      // 内容还没长回来时读到的 scrollTop 不是用户选的位置，既不能写存储，
      // 也不能当成“用户接管”。
      if (!sampleIsTrustworthy(current)) return
      if (restoring) {
        // 恢复途中用户自己滚了：一律以用户为准（哪怕一次跳得很远，也是他真实的
        // 滚轮 / 触摸 / 翻页操作）。位置仍要看住：列表随后切换渲染方式时容器
        // 高度还是会塌，塌了要贴回“用户自己选的位置”。
        if (!userDriven || Math.abs(current - saved) <= 1) return
        restoring = false
        adoptScrollPosition(current)
        startGuard(current)
        return
      }
      // 非恢复状态下，一瞬间大幅向上跳（虚拟列表挂载量高、浏览器钳制）只有按键
      // 翻页 / 拖滚动条这类意图才可能是用户行为，否则直接丢弃。
      const upJump = lastSaved > current ? lastSaved - current : 0
      if (upJump > maxSmoothJumpUp(el.clientHeight) && !(userDriven && userScrollAllowsJump)) {
        return
      }
      // 非用户滚动（恢复逻辑回填、虚拟列表挂载/量高、浏览器钳制）不写存储。
      if (!userDriven) return
      const height = el.scrollHeight
      if (
        !shouldPersistScrollSample({
          current,
          lastSaved,
          height,
          lastHeight,
        })
      ) {
        return
      }
      lastHeight = height
      if (current === lastSaved) return
      lastSaved = current
      writeScrollPosition(key, current)
    }

    // 用户滚动意图：滚轮 / 触摸 = 平滑滚动；翻页键 / 拖滚动条 = 允许直接跳很远。
    const onScrollIntent = () => {
      markUserScroll(false)
    }

    const onJumpIntent = () => {
      markUserScroll(true)
    }

    if (saved <= 0) {
      el.scrollTop = 0
    } else {
      if (typeof ResizeObserver !== 'undefined' && content) {
        observer = new ResizeObserver(onContentSizeChange)
        observer.observe(content)
      }
      tryRestore()
      if (restoring) {
        restorePumpUntil = performance.now() + 1000
        restoreFrame = window.requestAnimationFrame(pumpRestore)
        restoreTimer = window.setTimeout(finishRestore, 10000)
      }
    }

    const onPointerDown = (event: PointerEvent) => {
      // 点在容器本身 = 拖滚动条或在空白处按下：允许大幅跳转。
      if (event.target === el) {
        onJumpIntent()
      }
      saveNow()
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (isJumpIntentKey(event.key)) {
        onJumpIntent()
      } else if (isScrollIntentKey(event.key)) {
        onScrollIntent()
      }
      saveNow()
    }

    el.addEventListener('scroll', saveNow, { passive: true })
    el.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('wheel', onScrollIntent, { passive: true, capture: true })
    window.addEventListener('touchstart', onScrollIntent, { passive: true, capture: true })
    window.addEventListener('keydown', onKeyDown, true)

    return () => {
      el.removeEventListener('scroll', saveNow)
      el.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('wheel', onScrollIntent, true)
      window.removeEventListener('touchstart', onScrollIntent, true)
      window.removeEventListener('keydown', onKeyDown, true)
      stopRestore()
      // 清理时写入最后一次有效位置，避免依赖已被钳制的 el.scrollTop。
      writeScrollPosition(key, lastSaved)
    }
  }, [routeKey, userKey])
}
