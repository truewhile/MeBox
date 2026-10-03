// 刷新令牌并发保护与失败分类的回归测试。
// 纯逻辑，用 node 直接跑：node src/utils/authRefresh.test.ts
// （对齐 utils/mediaVersion.test.ts 的写法）。
import {
  RefreshRequestError,
  createRefreshCoordinator,
  createSingleFlightRunner,
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
