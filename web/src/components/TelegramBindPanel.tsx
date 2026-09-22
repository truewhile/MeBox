import { useCallback, useEffect, useRef, useState } from 'react'
import toast from 'react-hot-toast'
import { Check, Copy, Loader2, Send, Unlink } from 'lucide-react'

import { telegramAPI, type TelegramStatus } from '../api/telegram'

// TelegramBindPanel 完成「网页生成一次性码 → Bot 内 /bind」的绑定流程。
//
// 绑定成功后，新设备登录、被踢下线、账号即将到期等事件都会推到用户的
// Telegram，避免这些动作变成静默行为。
const POLL_INTERVAL_MS = 3000

export function TelegramBindPanel() {
  const [status, setStatus] = useState<TelegramStatus | null>(null)
  const [code, setCode] = useState('')
  const [expiresIn, setExpiresIn] = useState(0)
  const [starting, setStarting] = useState(false)
  const [unbinding, setUnbinding] = useState(false)
  const [copied, setCopied] = useState(false)
  const mountedRef = useRef(true)

  const refreshStatus = useCallback(async () => {
    try {
      const next = await telegramAPI.status()
      if (mountedRef.current) setStatus(next)
    } catch {
      if (mountedRef.current) setStatus({ bound: false })
    }
  }, [])

  useEffect(() => {
    mountedRef.current = true
    void refreshStatus()
    return () => {
      mountedRef.current = false
    }
  }, [refreshStatus])

  // 倒计时归零即作废本地展示的码，避免用户拿着过期码反复尝试。
  useEffect(() => {
    if (expiresIn <= 0) return
    const timer = setInterval(() => {
      setExpiresIn((prev) => {
        if (prev <= 1) {
          clearInterval(timer)
          return 0
        }
        return prev - 1
      })
    }, 1000)
    return () => clearInterval(timer)
  }, [expiresIn])

  // 等待用户在 Bot 里完成绑定：只在存在待消费的码时轮询。
  useEffect(() => {
    if (!code || expiresIn <= 0 || status?.bound) return
    const timer = setInterval(() => {
      void refreshStatus()
    }, POLL_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [code, expiresIn, status?.bound, refreshStatus])

  useEffect(() => {
    if (status?.bound && code) {
      setCode('')
      setExpiresIn(0)
      toast.success('Telegram 绑定成功')
    }
  }, [status?.bound, code])

  const startBind = async () => {
    if (starting) return
    setStarting(true)
    setCopied(false)
    try {
      const res = await telegramAPI.startBind()
      setCode(res.code)
      setExpiresIn(res.expires_in_seconds)
    } catch {
      toast.error('生成绑定码失败，请检查服务端是否可用')
    } finally {
      setStarting(false)
    }
  }

  const copyCode = async () => {
    if (!code) return
    try {
      await navigator.clipboard.writeText(`/bind ${code}`)
      setCopied(true)
      toast.success('已复制 /bind 指令')
      setTimeout(() => setCopied(false), 2000)
    } catch {
      toast.error('复制失败，请手动输入')
    }
  }

  const unbind = async () => {
    setUnbinding(true)
    try {
      await telegramAPI.unbind()
      setCode('')
      setExpiresIn(0)
      await refreshStatus()
      toast.success('已解除绑定')
    } catch {
      toast.error('解除绑定失败')
    } finally {
      setUnbinding(false)
    }
  }

  const bound = Boolean(status?.bound)

  return (
    <section className="glass-panel space-y-4">
      <div>
        <h2 className="flex items-center gap-2 font-display text-lg font-semibold text-ink-600">
          <Send size={20} className="text-brand-500" />
          Telegram 通知
        </h2>
        <p className="mt-1 text-sm text-ink-50">
          绑定后，新设备登录、设备被踢下线、账号即将到期等事件会推送到你的 Telegram。
          未绑定时这些事件只记录在服务端日志中。
        </p>
      </div>

      {bound ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-emerald-200 bg-emerald-50/40 p-4">
          <div className="text-sm text-ink-600">
            <span className="font-semibold">已绑定</span>
            {status?.chat_id_masked && (
              <span className="ml-2 font-mono text-xs text-ink-50">
                {status.chat_id_masked}
              </span>
            )}
          </div>
          <button
            type="button"
            onClick={() => void unbind()}
            disabled={unbinding}
            className="inline-flex items-center gap-2 rounded-xl border border-[var(--app-border)] px-3 py-2 text-sm font-semibold text-[var(--app-muted)] transition-colors hover:border-rose-300 hover:text-rose-500 disabled:opacity-40"
          >
            {unbinding ? <Loader2 size={16} className="animate-spin" /> : <Unlink size={16} />}
            解除绑定
          </button>
        </div>
      ) : (
        <div className="space-y-3">
          <ol className="list-decimal space-y-1 pl-5 text-sm text-ink-50">
            <li>点击下方按钮生成 6 位绑定码（5 分钟内有效）。</li>
            <li>在 Telegram 里打开 MeBox Bot，发送 <code className="font-mono">/bind 绑定码</code>。</li>
            <li>收到「绑定成功」回复即完成，本页会自动刷新状态。</li>
          </ol>

          {code && expiresIn > 0 ? (
            <div className="flex flex-wrap items-center gap-3 rounded-2xl border border-brand-200 bg-brand-50/40 p-4">
              <span className="select-all font-mono text-2xl font-black tracking-widest text-brand-600">
                {code}
              </span>
              <button
                type="button"
                onClick={() => void copyCode()}
                className="inline-flex items-center gap-1.5 rounded-lg bg-brand-100 px-3 py-1.5 text-xs font-semibold text-brand-700 transition-colors hover:bg-brand-200"
              >
                {copied ? <Check size={14} /> : <Copy size={14} />}
                {copied ? '已复制' : '复制 /bind 指令'}
              </button>
              <span className="text-xs text-ink-50">
                剩余 {Math.floor(expiresIn / 60)}:
                {(expiresIn % 60).toString().padStart(2, '0')}
              </span>
            </div>
          ) : (
            <button
              type="button"
              onClick={() => void startBind()}
              disabled={starting}
              className="neon-button"
            >
              {starting ? <Loader2 size={16} className="animate-spin" /> : <Send size={16} />}
              生成绑定码
            </button>
          )}
        </div>
      )}
    </section>
  )
}
