// 令牌刷新 API 模块。
import { RefreshRequestError, type RefreshTokens } from '../utils/authRefresh'

const REFRESH_ENDPOINT = '/api/auth/refresh'

// 刷新是短请求：网关/服务重启时尽快失败并让调用方重试，
// 不要用默认的长超时把页面卡在等待里。
const REFRESH_TIMEOUT_MS = 15_000

/**
 * 用 refresh token 换取新的令牌对。
 *
 * 刻意使用 fetch 而不是共享的 axios 实例：
 *  - 刷新请求的失败不能进入 401 拦截器，否则会递归触发刷新/登出；
 *  - 调用方需要拿到 HTTP 状态码，以区分「凭证失效」和「服务暂时不可用」。
 *
 * 服务端响应形如 { code, message, data: { token, refresh_token, ... } }。
 */
export async function requestRefreshTokens(refreshToken: string): Promise<RefreshTokens> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), REFRESH_TIMEOUT_MS)

  let resp: Response
  try {
    resp = await fetch(REFRESH_ENDPOINT, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      cache: 'no-store',
      body: JSON.stringify({ refresh_token: refreshToken }),
      signal: controller.signal,
    })
  } catch {
    // 网络不可达、被中止或超时：没有 HTTP 状态码，按「暂时不可用」处理。
    throw new RefreshRequestError('refresh request failed')
  } finally {
    clearTimeout(timer)
  }

  const body = (await resp.json().catch(() => null)) as
    | { code?: number; message?: string; data?: Partial<RefreshTokens> }
    | null

  if (!resp.ok) {
    throw new RefreshRequestError(body?.message ?? 'refresh failed', resp.status)
  }

  const token = body?.data?.token
  const nextRefreshToken = body?.data?.refresh_token
  if (!token || !nextRefreshToken) {
    // 200 但没有可用令牌（例如被网关/代理改写了响应）：同样按暂时不可用处理，
    // 不要据此清空用户会话。
    throw new RefreshRequestError('malformed refresh response')
  }
  return { token, refresh_token: nextRefreshToken }
}
