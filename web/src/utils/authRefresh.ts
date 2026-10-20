// 刷新令牌的并发保护与失败分类。
//
// 这里是纯逻辑（不 import 任何模块），便于用 node 直接跑回归测试
// （对齐 utils/mediaVersion.test.ts 的写法）。
//
// 背景：refresh token 是一次性凭证，服务端刷新成功即作废旧 token。真实
// 客户端却天然会并发使用同一个令牌——同一标签页里 WebSocket 重连与 401
// 拦截器、多个标签页共享一份 localStorage、部署/重启后多个页面同时刷新。
// 此前第二个请求必然拿到 401，前端把 401 当成会话失效并清空本地令牌，
// 外部表现就是「每次部署之后都要重新登录」。本模块负责：
//
//   1. 把刷新收敛成单次执行（single-flight + 浏览器跨标签页锁）；
//   2. 只在服务端明确拒绝凭证时才清空会话，网络/网关故障保留会话等重试。

/** 一次刷新尝试的结果。 */
export type RefreshOutcome = 'refreshed' | 'invalid' | 'transient'

/**
 * access token 过期前多久就视为失效。刷新要留出余量：刚好卡在 exp 上
 * 刷新，重放的原请求可能在服务端又已经过期。
 */
export const TOKEN_EXPIRY_SKEW_MS = 30_000

/**
 * 解析 JWT 的 exp 声明并换算成毫秒时间戳。
 *
 * 解析失败（非 JWT、base64url 损坏、缺少 exp、exp 不是数字）一律返回 null：
 * 调用方据此放行，绝不能把「读不懂」当成「已过期」，否则不透明的令牌会导致
 * 无休止的刷新循环。
 */
export function decodeJwtExpiryMs(token: string | null | undefined): number | null {
  if (!token) return null
  const segment = token.split('.')[1]
  if (!segment) return null
  try {
    // JWT 使用 base64url 编码，需先还原字符集并补齐 padding。
    const normalized = segment.replace(/-/g, '+').replace(/_/g, '/')
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=')
    const claims = JSON.parse(atob(padded)) as { exp?: unknown }
    return typeof claims.exp === 'number' ? claims.exp * 1000 : null
  } catch {
    return null
  }
}

/**
 * token 是否已过期或即将过期。
 *
 * 首屏放行前用它做一次判断：本地持久化的 token 往往已经过期，直接发请求只
 * 会一次性刷出一屏 401。读不出 exp 的令牌按「新鲜」处理（见 decodeJwtExpiryMs）。
 */
export function isAccessTokenStale(token: string | null | undefined, nowMs = Date.now()): boolean {
  const expiry = decodeJwtExpiryMs(token)
  if (expiry === null) return false
  return nowMs >= expiry - TOKEN_EXPIRY_SKEW_MS
}

/** 刷新接口返回的令牌对。 */
export interface RefreshTokens {
  token: string
  refresh_token: string
}

/** 刷新请求失败；status 缺失表示网络不可达/超时等非 HTTP 失败。 */
export class RefreshRequestError extends Error {
  readonly status?: number

  constructor(message: string, status?: number) {
    super(message)
    this.name = 'RefreshRequestError'
    this.status = status
  }
}

/**
 * 判断刷新失败属于「会话确实失效」还是「暂时不可用」。
 *
 * 只有服务端明确答复凭证无效才允许清空会话。部署/容器重启窗口里的连接
 * 失败、网关 502、超时都只是暂时不可用：把 token 留在本地等下一次重试，
 * 否则一次瞬时故障就会把用户永久踢到登录页。
 */
export function refreshFailureOutcome(status?: number): RefreshOutcome {
  if (typeof status !== 'number' || !Number.isFinite(status) || status <= 0) {
    return 'transient'
  }
  if (status === 408 || status === 429 || status >= 500) {
    return 'transient'
  }
  if (status === 400 || status === 401 || status === 403) {
    return 'invalid'
  }
  // 其他状态码（含网关/代理的非标准应答）都无法证明会话失效。
  return 'transient'
}

/** 从抛出的错误里取出 HTTP 状态码。 */
export function httpStatusOf(error: unknown): number | undefined {
  if (error instanceof RefreshRequestError) return error.status
  const status = (error as { status?: unknown } | null)?.status
  return typeof status === 'number' ? status : undefined
}

/**
 * 让并发调用共享同一次执行，结束后允许下一次重新执行。
 *
 * 这是同标签页内的收敛点：无论是 401 拦截器还是 WebSocket 重连触发刷新，
 * 都复用同一个在途请求，不会拿同一个一次性凭证发两次。
 */
export function createSingleFlightRunner<T>(task: () => Promise<T>): () => Promise<T> {
  let inflight: Promise<T> | null = null
  return () => {
    if (inflight) return inflight
    inflight = Promise.resolve()
      .then(task)
      .finally(() => {
        inflight = null
      })
    return inflight
  }
}

/**
 * 跨标签页互斥执行（Web Locks API）。
 *
 * 多个标签页共享同一份 localStorage 里的 refresh token，各自刷新的结果就是
 * 互相把对方打成 401。拿锁后只有第一个标签页真正发请求，其余标签页在锁内
 * 重新读取 localStorage，直接复用刚轮换出来的会话。
 * 浏览器不支持 Web Locks 时退化成纯单标签页收敛。
 */
export function createBrowserExclusiveRunner(
  lockName: string,
): <T>(task: () => Promise<T>) => Promise<T> {
  return <T>(task: () => Promise<T>): Promise<T> => {
    const locks = typeof navigator === 'undefined' ? undefined : navigator.locks
    if (!locks || typeof locks.request !== 'function') return task()
    try {
      return locks.request(lockName, () => task()) as Promise<T>
    } catch {
      return task()
    }
  }
}

export interface SessionGateDeps {
  /** 读取当前 access token（可能来自持久化存储，往往已经过期）。 */
  readAccessToken: () => string | null
  /** 刷新会话；语义与 useAuthStore.refreshSession 一致。 */
  refreshSession: () => Promise<RefreshOutcome>
  /** 可注入的时间源，便于测试。 */
  now?: () => number
}

/**
 * 构造请求前预检：每个请求发出前调用，必要时先把过期的令牌换新。
 *
 * 本地持久化的 access token 常常已经过期。此时若直接发请求，首屏那些各自
 * 独立的 useEffect 会在同一帧里并发打出受保护请求，全部拿到 401，控制台
 * 一次刷出一整屏红字，用户还要等 401 拦截器刷新完才能看到内容
 * （现场：2026-10-16 12:49，7 个请求同时 401）。
 *
 * 预检把「先刷新、再发送」放在请求发出之前：
 *   - token 新鲜（或无法解析 exp）→ 立即返回；
 *   - token 已过期 → 等待刷新（刷新协调器自带单飞，并发请求只发一次）；
 *   - 刷新遇到瞬时故障 → 照常发送（页面自己会重试）；
 *   - 服务端明确判定凭证失效 → 抛出，请求根本不发出去。
 *
 * 未登录（没有 token）直接返回：跳登录页是路由守卫的职责。
 */
export function createTokenPreflight(deps: SessionGateDeps): () => Promise<void> {
  return async () => {
    const token = deps.readAccessToken()
    if (!token) return
    const now = deps.now ?? Date.now
    if (!isAccessTokenStale(token, now())) return
    const outcome = await deps.refreshSession().catch((): RefreshOutcome => 'transient')
    if (outcome === 'invalid') {
      throw new Error('session expired')
    }
  }
}

export interface RefreshCoordinatorDeps {
  readAccessToken: () => string | null
  readRefreshToken: () => string | null
  applyTokens: (tokens: RefreshTokens) => void
  clearSession: () => void
  request: (refreshToken: string) => Promise<RefreshTokens>
  runExclusive: <T>(task: () => Promise<T>) => Promise<T>
  /** 重新读取持久化存储，用于拿到其他标签页写回的令牌。 */
  syncFromStorage: () => void | Promise<void>
}

/**
 * 构造全局唯一的刷新入口。返回的函数可以被任意并发调用，
 * 结果只会是 'refreshed' | 'invalid' | 'transient'。
 */
export function createRefreshCoordinator(
  deps: RefreshCoordinatorDeps,
): () => Promise<RefreshOutcome> {
  return createSingleFlightRunner(async (): Promise<RefreshOutcome> => {
    const tokenAtEntry = deps.readRefreshToken()
    // 没有 refresh token 就无从恢复会话。
    if (!tokenAtEntry) return 'invalid'

    return deps.runExclusive(async () => {
      // 其他标签页可能刚完成轮换：先同步它写回的令牌，
      // 否则本标签页会拿已经被服务端作废的旧 token 去刷新。
      await deps.syncFromStorage()
      const current = deps.readRefreshToken()
      if (!current) return 'invalid'
      if (current !== tokenAtEntry) {
        // 令牌已被其他标签页换新：直接用新会话，不再发请求。
        return deps.readAccessToken() ? 'refreshed' : 'invalid'
      }

      try {
        deps.applyTokens(await deps.request(current))
        return 'refreshed'
      } catch (error) {
        const outcome = refreshFailureOutcome(httpStatusOf(error))
        if (outcome === 'invalid') deps.clearSession()
        return outcome
      }
    })
  })
}
