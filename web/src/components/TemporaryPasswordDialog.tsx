import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { Check, Copy, Loader2, RefreshCw, Tv, X } from 'lucide-react'
import toast from 'react-hot-toast'

import { authAPI } from '../api/auth'
import { useAuthStore } from '../stores/auth'

export function TemporaryPasswordDialog({
  isOpen,
  onClose,
}: {
  isOpen: boolean
  onClose: () => void
}) {
  const user = useAuthStore((s) => s.user)
  const [code, setCode] = useState('')
  const [expiresIn, setExpiresIn] = useState(0)
  const [loading, setLoading] = useState(false)
  const [copied, setCopied] = useState(false)

  const fetchCode = async () => {
    setLoading(true)
    try {
      const res = await authAPI.createTemporaryPassword()
      setCode(res.code)
      setExpiresIn(res.expires_in)
      setCopied(false)
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '获取临时密码失败'
      toast.error(msg)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (isOpen) {
      fetchCode()
    } else {
      setCode('')
      setExpiresIn(0)
    }
  }, [isOpen])

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

  const copyCode = async () => {
    if (!code) return
    try {
      await navigator.clipboard.writeText(code)
      setCopied(true)
      toast.success('已复制到剪贴板')
      setTimeout(() => setCopied(false), 2000)
    } catch {
      toast.error('复制失败，请手动长按复制')
    }
  }

  if (!isOpen || typeof document === 'undefined') return null

  return createPortal(
    <div
      className="fixed inset-0 z-[999] flex items-center justify-center bg-black/65 p-3 sm:p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="w-full max-w-[calc(100vw-2rem)] sm:max-w-md max-h-[90dvh] flex flex-col overflow-hidden rounded-3xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4 sm:p-6 shadow-2xl transition-all"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-[var(--app-border)] pb-3 sm:pb-4 shrink-0">
          <div className="flex items-center gap-2.5 sm:gap-3 min-w-0">
            <div className="flex h-9 w-9 sm:h-10 sm:w-10 shrink-0 items-center justify-center rounded-2xl bg-brand-500/10 text-brand-500">
              <Tv size={20} className="sm:w-[22px] sm:h-[22px]" />
            </div>
            <div className="min-w-0">
              <h3 className="font-display text-base sm:text-lg font-bold text-[var(--app-text)] truncate">
                电视端临时登录码 (OTP)
              </h3>
              <p className="text-[11px] sm:text-xs text-[var(--app-muted)] truncate">
                适用于 Emby / Jellyfin / Infuse 客户端快速免密登录
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl p-1.5 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] shrink-0 ml-2"
          >
            <X size={18} />
          </button>
        </div>

        {/* Content */}
        <div className="my-4 space-y-3.5 sm:space-y-4 overflow-y-auto flex-1 pr-0.5">
          <div className="flex items-center justify-between text-[11px] sm:text-xs text-[var(--app-muted)]">
            <div>
              <span>登录账号：</span>
              <span className="font-mono font-bold text-[var(--app-text)]">{user?.username}</span>
            </div>
            {expiresIn > 0 ? (
              <div>
                <span>有效时间剩余：</span>
                <span className={`font-mono font-bold ${expiresIn < 60 ? 'text-red-500' : 'text-brand-500'}`}>
                  {Math.floor(expiresIn / 60).toString().padStart(2, '0')}:{(expiresIn % 60).toString().padStart(2, '0')}
                </span>
              </div>
            ) : null}
          </div>

          <div className="flex items-center justify-between rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] p-3 sm:p-4">
            {loading ? (
              <div className="flex h-10 w-full items-center justify-center text-[var(--app-muted)]">
                <Loader2 size={24} className="animate-spin text-brand-500" />
              </div>
            ) : code && expiresIn > 0 ? (
              <>
                <div className="font-mono text-2xl sm:text-3xl font-black tracking-widest text-brand-500 select-all">
                  {code}
                </div>
                <button
                  type="button"
                  onClick={copyCode}
                  className="flex items-center gap-1.5 rounded-xl bg-brand-500/10 px-3 sm:px-3.5 py-1.5 sm:py-2 text-xs font-semibold text-brand-500 transition-colors hover:bg-brand-500/20 shrink-0"
                >
                  {copied ? <Check size={14} /> : <Copy size={14} />}
                  {copied ? '已复制' : '复制密码'}
                </button>
              </>
            ) : (
              <div className="flex w-full items-center justify-between text-xs sm:text-sm text-[var(--app-muted)]">
                <span>登录码已失效或未生成</span>
                <button
                  type="button"
                  onClick={fetchCode}
                  className="flex items-center gap-1.5 rounded-xl bg-brand-500 px-3 py-1.5 text-xs font-semibold text-white shrink-0"
                >
                  <RefreshCw size={13} />
                  立即生成
                </button>
              </div>
            )}
          </div>

          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel-elevated)] p-3 text-[11px] sm:text-xs leading-relaxed text-[var(--app-muted)]">
            <span className="font-semibold text-[var(--app-text)]">使用方法：</span>
            在电视端或外部设备的 Emby 登录界面输入用户名 <code className="font-bold text-brand-500">{user?.username}</code> 和上方 6 位临时码。登录后临时码立即作废（一次性阅后即焚），客户端将自动获取长期持久令牌。
          </div>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between border-t border-[var(--app-border)] pt-3 sm:pt-4 shrink-0">
          <button
            type="button"
            onClick={fetchCode}
            disabled={loading}
            className="flex items-center gap-1.5 rounded-xl px-2.5 sm:px-3 py-1.5 sm:py-2 text-xs font-medium text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] disabled:opacity-50"
          >
            <RefreshCw size={13} className={loading ? 'animate-spin' : ''} />
            重新生成
          </button>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-elevated)] px-3.5 sm:px-4 py-1.5 sm:py-2 text-xs font-semibold text-[var(--app-text)] transition-colors hover:bg-[var(--app-hover)]"
          >
            完成并关闭
          </button>
        </div>
      </div>
    </div>,
    document.body
  )
}
