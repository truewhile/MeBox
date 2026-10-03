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
