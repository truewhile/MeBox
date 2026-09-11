import { useEffect, useRef } from 'react'

import { useAuthStore } from '../stores/auth'

// 前 5 次沿用原快速退避间隔；之后进入 60s 慢速重试并不再放弃，
// 服务重启或网络恢复后仍能自动重连（清理函数可随时取消定时器）。
const FAST_RECONNECT_ATTEMPTS = 5
const SLOW_RECONNECT_INTERVAL = 60_000

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
