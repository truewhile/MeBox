// 刷新令牌并发保护与失败分类的回归测试。
// 纯逻辑，用 node 直接跑：node src/utils/authRefresh.test.ts
// （对齐 utils/mediaVersion.test.ts 的写法）。
import {
  RefreshRequestError,
  createRefreshCoordinator,
  createSingleFlightRunner,
  createTokenPreflight,
  decodeJwtExpiryMs,
  isAccessTokenStale,
  refreshFailureOutcome,
  type RefreshOutcome,
  type RefreshTokens,
} from './authRefresh.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`authRefresh: ${name}`)
}

async function expectOutcome(name: string, promise: Promise<RefreshOutcome>, want: RefreshOutcome) {
  const got = await promise
  if (got !== want) throw new Error(`authRefresh: ${name} = ${got}, want ${want}`)
}

// ── 失败分类：只有服务端明确拒绝凭证才算会话失效 ──────────────────────────
check('401 is invalid', refreshFailureOutcome(401) === 'invalid')
check('403 is invalid', refreshFailureOutcome(403) === 'invalid')
check('400 is invalid', refreshFailureOutcome(400) === 'invalid')
check('network failure is transient', refreshFailureOutcome(undefined) === 'transient')
check('502 gateway is transient', refreshFailureOutcome(502) === 'transient')
check('503 gateway is transient', refreshFailureOutcome(503) === 'transient')
check('timeout is transient', refreshFailureOutcome(408) === 'transient')
check('429 is transient', refreshFailureOutcome(429) === 'transient')
check('unexpected status is transient', refreshFailureOutcome(418) === 'transient')

// ── JWT 过期判定：首屏放行前必须能识别出「已经过期的 access token」 ────────
// 现场（2026-10-16 12:49）：刷新页面时本地 token 已过期，React 首屏并发发出
// 7 个受保护请求，全部 401，控制台一次性刷出 7 行红字；约 2 秒后才由拦截器
// 刷新成功并重放。修复要求首屏就认得过期 token，先刷新再放行。
function jwtWithExp(exp: number | string | null): string {
  const encode = (value: unknown) =>
    Buffer.from(JSON.stringify(value)).toString('base64url')
  const claims: Record<string, unknown> = { uid: 'u1' }
  if (exp !== null) claims.exp = exp
  return `${encode({ alg: 'HS256', typ: 'JWT' })}.${encode(claims)}.signature`
}

const NOW = 1_700_000_000_000

check('jwt expiry is decoded to milliseconds',
  decodeJwtExpiryMs(jwtWithExp(NOW / 1000 + 3600)) === NOW + 3600_000)
check('jwt without exp decodes to null', decodeJwtExpiryMs(jwtWithExp(null)) === null)
check('jwt with non-numeric exp decodes to null', decodeJwtExpiryMs(jwtWithExp('soon')) === null)
check('opaque token decodes to null', decodeJwtExpiryMs('not-a-jwt') === null)
check('empty token decodes to null', decodeJwtExpiryMs('') === null)
check('malformed payload decodes to null', decodeJwtExpiryMs('aaa.!!!.ccc') === null)

// 已过期 / 即将过期的 token 必须在首屏就被拦下。
check('expired token is stale', isAccessTokenStale(jwtWithExp(NOW / 1000 - 1), NOW))
check('token expiring inside the skew is stale',
  isAccessTokenStale(jwtWithExp(NOW / 1000 + 20), NOW))
check('token outside the skew is fresh',
  isAccessTokenStale(jwtWithExp(NOW / 1000 + 3600), NOW) === false)
// 边界：正好落在 skew 上算过期（留出余量，避免刷新途中再次过期）。
check('token exactly at the skew boundary is stale',
  isAccessTokenStale(jwtWithExp(NOW / 1000 + 30), NOW))

// 没有 token 或 token 无法解析时不能判定为「过期」：
// 前者由路由守卫处理，后者强行刷新会变成无休止的刷新循环。
check('missing token is not stale', isAccessTokenStale(null, NOW) === false)
check('opaque token is not stale', isAccessTokenStale('opaque', NOW) === false)

// ── 发送前预检：本地 token 已过期就先刷新，再让请求带新令牌出去 ────────────
// 现场（2026-10-16 12:49）：刷新页面时本地 token 已过期，React 首屏并发发出
// 7 个受保护请求，全部 401，控制台一次刷出 7 行红字；约 2 秒后才由 401 拦截器
// 刷新成功并重放。修复要求请求在发出前就认得过期 token，先刷新再发送。
async function expectPreflight(
  name: string,
  run: Promise<void>,
  want: 'resolved' | 'rejected',
) {
  let outcome: 'resolved' | 'rejected' = 'resolved'
  try {
    await run
  } catch {
    outcome = 'rejected'
  }
  if (outcome !== want) throw new Error(`authRefresh: ${name} = ${outcome}, want ${want}`)
}

function preflightHarness(overrides: {
  accessToken?: string | null
  refresh?: () => Promise<RefreshOutcome>
}) {
  const state = { token: overrides.accessToken === undefined ? 'at-1' : overrides.accessToken }
  const calls = { refresh: 0 }
  const ensure = createTokenPreflight({
    readAccessToken: () => state.token,
    refreshSession: async () => {
      calls.refresh += 1
      if (overrides.refresh) return overrides.refresh()
      state.token = jwtWithExp(Date.now() / 1000 + 3600)
      return 'refreshed'
    },
    now: () => NOW,
  })
  return { state, calls, ensure }
}

{
  // 令牌新鲜：不得产生任何刷新请求。
  const { calls, ensure } = preflightHarness({ accessToken: jwtWithExp(NOW / 1000 + 3600) })
  await expectPreflight('fresh token passes straight through', ensure(), 'resolved')
  check('fresh token skips the refresh', calls.refresh === 0)
}

{
  // 核心回归：过期令牌必须先刷新，且刷新完成后才返回（调用方据此发请求）。
  const { state, calls, ensure } = preflightHarness({ accessToken: jwtWithExp(NOW / 1000 - 60) })
  await expectPreflight('stale token refreshes before sending', ensure(), 'resolved')
  check('stale token refreshed exactly once', calls.refresh === 1)
  check('preflight handed out a fresh token',
    !isAccessTokenStale(state.token, NOW))
}

{
  // 并发首屏：7 个请求同时预检，只允许刷新一次（协调器本身是单飞，
  // 这里确认预检不会绕过它）。
  const { calls, ensure } = preflightHarness({ accessToken: jwtWithExp(NOW / 1000 - 60) })
  await Promise.all([ensure(), ensure(), ensure(), ensure(), ensure(), ensure(), ensure()])
  check('concurrent preflight refreshes once', calls.refresh === 1)
}

{
  // 瞬时故障（部署/重启窗口）：保留会话，照常发出请求——
  // 页面自己会重试，不该把人挡在门外。
  const { ensure } = preflightHarness({
    accessToken: jwtWithExp(NOW / 1000 - 60),
    refresh: async () => 'transient',
  })
  await expectPreflight('transient failure still sends the request', ensure(), 'resolved')
}

{
  // 服务端明确判定凭证失效：不发请求，直接失败，避免刷出无意义的 401。
  const { ensure } = preflightHarness({
    accessToken: jwtWithExp(NOW / 1000 - 60),
    refresh: async () => 'invalid',
  })
  await expectPreflight('revoked session refuses to send', ensure(), 'rejected')
}

{
  // 未登录：交给路由守卫和 401 路径处理，预检不替它们做决定。
  const { calls, ensure } = preflightHarness({ accessToken: null })
  await expectPreflight('anonymous request is not preflighted', ensure(), 'resolved')
  check('anonymous request does not refresh', calls.refresh === 0)
}

{
  // 非 JWT 令牌（Emby 等外部客户端签发）：读不出 exp，按新鲜处理，
  // 绝不能因此陷入刷新循环。
  const { calls, ensure } = preflightHarness({ accessToken: 'opaque-token' })
  await expectPreflight('opaque token passes through', ensure(), 'resolved')
  check('opaque token does not refresh', calls.refresh === 0)
}

{
  // 刷新抛出异常（网络不可达）：按瞬时故障处理，请求照发。
  const { ensure } = preflightHarness({
    accessToken: jwtWithExp(NOW / 1000 - 60),
    refresh: async () => {
      throw new Error('network down')
    },
  })
  await expectPreflight('throwing refresh still sends the request', ensure(), 'resolved')
}

// ── 单飞：并发调用共享同一次执行，结束后可重新执行 ────────────────────────
{
  let calls = 0
  let release: (() => void) | null = null
  const run = createSingleFlightRunner(async () => {
    calls += 1
    const current = calls
    // 只有第一次执行停在闸门上，用来观察「在途期间不会重复执行」。
    if (current === 1) {
      await new Promise<void>((resolve) => {
        release = resolve
      })
    }
    return current
  })

  const first = run()
  const second = run()
  check('single flight shares one promise', first === second)
  // 任务体在下一个微任务才开始，排在其后放行即可确保 release 已就绪。
  queueMicrotask(() => release?.())
  check('single flight result', (await first) === 1)
  check('single flight stayed single', calls === 1)
  check('single flight re-runs after settle', (await run()) === 2)
}

// ── 协调器 ────────────────────────────────────────────────────────────────
function coordinatorHarness(overrides: {
  refreshToken?: string | null
  accessToken?: string | null
  request?: (token: string) => Promise<RefreshTokens>
  onApply?: (tokens: RefreshTokens) => void
  onClear?: () => void
  syncFromStorage?: () => void
}) {
  const state = {
    refreshToken: overrides.refreshToken === undefined ? 'rt-1' : overrides.refreshToken,
    accessToken: overrides.accessToken === undefined ? 'at-1' : overrides.accessToken,
    applied: [] as RefreshTokens[],
    cleared: 0,
    requests: [] as string[],
  }
  const refreshSession = createRefreshCoordinator({
    readAccessToken: () => state.accessToken,
    readRefreshToken: () => state.refreshToken,
    applyTokens: (tokens) => {
      state.applied.push(tokens)
      state.accessToken = tokens.token
      state.refreshToken = tokens.refresh_token
      overrides.onApply?.(tokens)
    },
    clearSession: () => {
      state.cleared += 1
      state.accessToken = null
      state.refreshToken = null
      overrides.onClear?.()
    },
    request: async (token) => {
      state.requests.push(token)
      return overrides.request ? overrides.request(token) : { token: 'at-2', refresh_token: 'rt-2' }
    },
    runExclusive: (task) => task(),
    syncFromStorage: () => overrides.syncFromStorage?.(),
  })
  return { state, refreshSession }
}

// 并发刷新只发一次请求，且都拿到成功结果（部署后 WS 重连 + 拦截器同时刷新）。
{
  const { state, refreshSession } = coordinatorHarness({})
  const [a, b] = await Promise.all([refreshSession(), refreshSession()])
  check('concurrent refresh both succeed', a === 'refreshed' && b === 'refreshed')
  check('concurrent refresh sends one request', state.requests.length === 1)
  check('concurrent refresh applies once', state.applied.length === 1)
}

// 服务暂时不可用（502 / 网络错误）时保留会话，不清空本地令牌。
{
  const { state, refreshSession } = coordinatorHarness({
    request: async () => {
      throw new RefreshRequestError('bad gateway', 502)
    },
  })
  await expectOutcome('gateway error', refreshSession(), 'transient')
  check('gateway error keeps session', state.cleared === 0 && state.refreshToken === 'rt-1')
}
{
  const { state, refreshSession } = coordinatorHarness({
    request: async () => {
      throw new RefreshRequestError('network down')
    },
  })
  await expectOutcome('network error', refreshSession(), 'transient')
  check('network error keeps session', state.cleared === 0 && state.refreshToken === 'rt-1')
}

// 服务端明确判定凭证失效（401 revoked）时才清空会话。
{
  const { state, refreshSession } = coordinatorHarness({
    request: async () => {
      throw new RefreshRequestError('refresh token revoked', 401)
    },
  })
  await expectOutcome('revoked token', refreshSession(), 'invalid')
  check('revoked token clears session', state.cleared === 1 && state.refreshToken === null)
}

// 没有 refresh token 时直接判定失效，不发请求。
{
  const { state, refreshSession } = coordinatorHarness({ refreshToken: null })
  await expectOutcome('missing refresh token', refreshSession(), 'invalid')
  check('missing refresh token sends no request', state.requests.length === 0)
}

// 另一个标签页已经换过令牌：直接复用，不能拿旧 token 再刷（那会被判 401）。
{
  const { state, refreshSession } = coordinatorHarness({
    syncFromStorage: () => {
      state.refreshToken = 'rt-other-tab'
      state.accessToken = 'at-other-tab'
    },
  })
  await expectOutcome('other tab already rotated', refreshSession(), 'refreshed')
  check('other tab rotation sends no request', state.requests.length === 0)
  check('other tab rotation keeps new session', state.refreshToken === 'rt-other-tab')
}

// 另一个标签页登出了：本标签页也应判定失效。
{
  const { state, refreshSession } = coordinatorHarness({
    syncFromStorage: () => {
      state.refreshToken = null
      state.accessToken = null
    },
  })
  await expectOutcome('other tab logged out', refreshSession(), 'invalid')
  check('other tab logout sends no request', state.requests.length === 0)
}

// 瞬时故障后仍可重试：会话没被清空，下一次刷新能成功。
{
  let attempt = 0
  const { state, refreshSession } = coordinatorHarness({
    request: async () => {
      attempt += 1
      if (attempt === 1) throw new RefreshRequestError('network down')
      return { token: 'at-retry', refresh_token: 'rt-retry' }
    },
  })
  await expectOutcome('first transient attempt', refreshSession(), 'transient')
  await expectOutcome('retry after transient', refreshSession(), 'refreshed')
  check('retry used the same token', state.requests[1] === 'rt-1')
  check('retry applied new tokens', state.refreshToken === 'rt-retry')
}

console.log('authRefresh.test.ts ok')
