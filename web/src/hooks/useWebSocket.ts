import { useEffect, useRef } from 'react'

import { useAuthStore } from '../stores/auth'

// 前 5 次沿用原快速退避间隔；之后进入 60s 慢速重试并不再放弃，
// 服务重启或网络恢复后仍能自动重连（清理函数可随时取消定时器）。
const FAST_RECONNECT_ATTEMPTS = 5
const SLOW_RECONNECT_INTERVAL = 60_000

// token 过期前 30 秒即视为失效，给刷新留出余量。
const TOKEN_EXPIRY_SKEW_MS = 30_000

// decodeJwtExpiryMs 解析 JWT 的 exp 声明并换算成毫秒时间戳。
// 解析失败（非 JWT / 结构异常）返回 null，调用方据此放行重连。
function decodeJwtExpiryMs(token: string): number | null {
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

// isTokenExpired 判断 token 是否已过期或即将过期。
function isTokenExpired(token: string): boolean {
  const expiry = decodeJwtExpiryMs(token)
  if (expiry === null) return false
  return Date.now() >= expiry - TOKEN_EXPIRY_SKEW_MS
}

// useWebSocket opens a single connection to /api/ws and dispatches every
// message to the supplied handler. Auto-reconnects with back-off while the
// auth token is present; after the fast retries are exhausted it keeps a
// slow 60s retry loop instead of giving up permanently.
export function useWebSocket(onEvent: (topic: string, payload: unknown) => void) {
  const ref = useRef<WebSocket | null>(null)
  const token = useAuthStore((s) => s.token)
  const onEventRef = useRef(onEvent)

  useEffect(() => {
    onEventRef.current = onEvent
  }, [onEvent])

  useEffect(() => {
    if (!token) return
    let closed = false
    let timer: number | undefined
    let reconnectAttempts = 0

    const open = () => {
      if (closed) return
      // 过期的 token 握手必然 401。此前这里会以 60s 间隔无限重试，服务端
      // 日志里表现为每分钟一条 401。改为先走刷新流程：成功会更新 token 并
      // 让本 effect 重建连接，失败则清空会话停止重连。
      if (isTokenExpired(token)) {
        void useAuthStore.getState().tokenRefresh()
        return
      }
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const url = `${proto}//${window.location.host}/api/ws?token=${encodeURIComponent(token)}`
      const ws = new WebSocket(url)
      ref.current = ws
      ws.onopen = () => {
        reconnectAttempts = 0
      }
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data)
          if (msg && typeof msg.topic === 'string') {
            onEventRef.current(msg.topic, msg.payload)
          }
        } catch {
          // ignore malformed frames
        }
      }
      ws.onclose = () => {
        if (closed) return
        // token 过期时不要继续退避重试，交给刷新流程处理。
        if (isTokenExpired(token)) {
          void useAuthStore.getState().tokenRefresh()
          return
        }
        reconnectAttempts += 1
        // 快速阶段保持原有线性退避，之后固定 60s 慢速重试；
        // timer 始终只有一个在途，cleanup 时统一清除，不会堆积。
        const delay =
          reconnectAttempts <= FAST_RECONNECT_ATTEMPTS
            ? Math.min(3_000 * reconnectAttempts, 30_000)
            : SLOW_RECONNECT_INTERVAL
        timer = window.setTimeout(open, delay)
      }
    }

    open()
    return () => {
      closed = true
      if (timer) window.clearTimeout(timer)
      ref.current?.close()
    }
  }, [token])
}
