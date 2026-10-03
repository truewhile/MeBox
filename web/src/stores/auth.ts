import { create } from 'zustand'
import { persist } from 'zustand/middleware'

import type { User } from '../types'
import { requestRefreshTokens } from '../api/refresh'
import {
  createBrowserExclusiveRunner,
  createRefreshCoordinator,
  type RefreshOutcome,
} from '../utils/authRefresh'

// Single source of truth for the authenticated user + JWT.
// Persisted to localStorage so a page reload does not drop the session.
interface AuthState {
  token: string | null
  refreshToken: string | null
  user: User | null
  tier: string
  setSession: (token: string, refreshToken: string, user: User) => void
  setUser: (user: User) => void
  setToken: (token: string) => void
  setRefreshToken: (refreshToken: string) => void
  logout: () => void
  /**
   * 刷新会话的唯一入口（同标签页 + 跨标签页收敛成一次请求）。
   *
   * 返回的 outcome 决定调用方怎么处理：
   *  - 'refreshed' 已拿到新令牌，可重试原请求；
   *  - 'invalid'   服务端明确判定凭证失效，本地会话已清空，需重新登录；
   *  - 'transient' 网络/服务暂时不可用，本地会话保留，稍后重试即可。
   */
  refreshSession: () => Promise<RefreshOutcome>
  /** 兼容旧调用方：只在真的换到新令牌时返回 true。 */
  tokenRefresh: () => Promise<boolean>
}

export const AUTH_STORAGE_KEY = 'mebox-auth'

// 跨标签页互斥：多个标签页共享同一份 refresh token，
// 只允许其中一个真正发起刷新。
const runRefreshExclusive = createBrowserExclusiveRunner('mebox-auth-refresh')

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => {
      const refreshSession = createRefreshCoordinator({
        readAccessToken: () => get().token,
        readRefreshToken: () => get().refreshToken,
        applyTokens: (tokens) =>
          set({ token: tokens.token, refreshToken: tokens.refresh_token }),
        clearSession: () => set({ token: null, refreshToken: null, user: null, tier: 'free' }),
        request: requestRefreshTokens,
        runExclusive: runRefreshExclusive,
        // 其他标签页刷新成功后会写回 localStorage；重新读进来，避免本标签页
        // 继续持有已被服务端轮换作废的旧 refresh token。
        syncFromStorage: () => useAuthStore.persist.rehydrate(),
      })

      return {
        token: null,
        refreshToken: null,
        user: null,
        tier: 'free',
        setSession: (token, refreshToken, user) => set({
          token,
          refreshToken,
          user,
          tier: user.tier || 'free'
        }),
        setUser: (user) => set({ user, tier: user.tier || 'free' }),
        setToken: (token) => set({ token }),
        setRefreshToken: (refreshToken) => set({ refreshToken }),
        logout: () => set({ token: null, refreshToken: null, user: null, tier: 'free' }),
        refreshSession,
        tokenRefresh: async () => (await refreshSession()) === 'refreshed',
      }
    },
    {
      name: AUTH_STORAGE_KEY,
      partialize: (state) => ({
        token: state.token,
        refreshToken: state.refreshToken,
        user: state.user,
        tier: state.tier
      }),
    },
  ),
)

// storage 事件只在其他标签页写入时触发：登出/轮换后同步本地会话，
// 否则本标签页会继续用已失效的令牌发请求。
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event) => {
    if (event.key === AUTH_STORAGE_KEY) {
      void useAuthStore.persist.rehydrate()
    }
  })
}

// Helper function to check if user is authenticated
export function isAuthenticated(): boolean {
  return useAuthStore.getState().token !== null
}

// Helper function to check if user is admin
export function isAdmin(): boolean {
  const user = useAuthStore.getState().user
  return user?.role === 'admin'
}

// Helper function to check if user is plus
export function isPlus(): boolean {
  const state = useAuthStore.getState()
  return state.tier === 'plus' || state.user?.role === 'admin'
}

// Helper function to check if user is super user (admin or plus)
export function isSuperUser(): boolean {
  return isAdmin() || isPlus()
}
