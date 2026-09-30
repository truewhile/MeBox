import { useCallback, useEffect, useMemo, useState } from 'react'
import toast from 'react-hot-toast'
import { ExternalLink, KeyRound, Loader2, LogOut, RefreshCw, Save, X } from 'lucide-react'

import { readerAPI, type ReaderLoginField, type ReaderSourceLogin } from '../../api/reader'

// 书源登录面板（仿 legado SourceLoginDialog）。
//
// 后端按 legado 的登录模型执行：loginUi 是表单描述，loginUrl 是登录逻辑，
// 点按钮时不区分类型，统统把 action 拼在 loginUrl 之后执行。
// 服务端没有弹窗，java.toast/longToast 的提示与 java.startBrowser 的地址
// 都通过返回值回传，这里负责展示并代为打开浏览器。

interface Props {
  sourceId: string
  sourceName: string
  onClose: () => void
  onLoggedInChange?: (loggedIn: boolean) => void
}

// formValues 取非按钮控件的初始值（已保存值优先，其次 default）。
function initialFormValues(info: ReaderSourceLogin): Record<string, string> {
  const out: Record<string, string> = {}
  for (const f of info.fields) {
    if (f.type === 'button') continue
    out[f.name] = info.values?.[f.name] ?? f.default ?? ''
  }
  return out
}

export default function SourceLoginDialog({ sourceId, sourceName, onClose, onLoggedInChange }: Props) {
  const [info, setInfo] = useState<ReaderSourceLogin | null>(null)
  const [loading, setLoading] = useState(true)
  const [running, setRunning] = useState('')
  const [form, setForm] = useState<Record<string, string>>({})
  const [toasts, setToasts] = useState<string[]>([])
  const [error, setError] = useState('')
  const [variable, setVariable] = useState('')
  const [showVariable, setShowVariable] = useState(false)
  const [showLoginJS, setShowLoginJS] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await readerAPI.sourceLogin(sourceId)
      setInfo(data)
      setForm(initialFormValues(data))
      setVariable(data.variable ?? '')
      setError(data.error ?? '')
    } catch (e) {
      setError((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '读取登录信息失败')
    } finally {
      setLoading(false)
    }
  }, [sourceId])

  useEffect(() => {
    void load()
  }, [load])

  // 按钮分为「登录动作」与「其他工具按钮」两类，便于排版。
  const buttons = useMemo(() => info?.fields.filter((f) => f.type === 'button') ?? [], [info])
  const inputs = useMemo(() => info?.fields.filter((f) => f.type !== 'button') ?? [], [info])
  const selects = useMemo(() => info?.fields.filter((f) => f.type === 'toggle' || f.type === 'select') ?? [], [info])

  const runAction = async (action: string, opts: { saveFields?: boolean } = {}) => {
    const key = action || '__login__'
    setRunning(key)
    setToasts([])
    setError('')
    try {
      const res = await readerAPI.runSourceLogin(sourceId, { action, fields: form })
      const messages = [...(res.toasts ?? [])]
      if (res.error) messages.push(res.error)
      setToasts(messages)
      if (res.browsers?.length) {
        // 服务端无法弹窗：把需要人工操作的页面提示给用户
        for (const b of res.browsers) {
          messages.push(`需要浏览器操作：${b.title || b.url}`)
        }
        setToasts([...messages])
      }
      // 同步回最新登录信息（书源可能在动作里回填字段）
      if (res.values) setForm((prev) => ({ ...prev, ...res.values }))
      if (opts.saveFields && !res.ok) {
        // 动作失败也保留用户输入，便于改完重试
      }
      onLoggedInChange?.(res.logged_in)
      const nextInfo = await readerAPI.sourceLogin(sourceId)
      setInfo(nextInfo)
      setVariable(nextInfo.variable ?? '')
      if (res.logged_in) toast.success('登录成功')
      else if (!res.ok) toast.error('登录失败')
    } catch (e) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '登录执行失败'
      setError(msg)
      toast.error(msg)
    } finally {
      setRunning('')
    }
  }

  const saveLoginInfo = async () => {
    try {
      await readerAPI.saveSourceLoginInfo(sourceId, form)
      toast.success('已保存登录信息')
    } catch {
      toast.error('保存失败')
    }
  }

  const doLogout = async () => {
    if (!window.confirm('退出登录并清除该源已保存的 Cookie？')) return
    try {
      await readerAPI.logoutSource(sourceId)
      toast.success('已退出登录')
      onLoggedInChange?.(false)
      await load()
    } catch {
      toast.error('退出失败')
    }
  }

  const saveVariable = async () => {
    try {
      await readerAPI.setSourceVariable(sourceId, variable)
      toast.success('源变量已保存')
      const next = await readerAPI.sourceLogin(sourceId)
      setInfo(next)
    } catch (e) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '保存失败'
      toast.error(msg)
    }
  }

  const openExternal = (url: string) => {
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  // 已保存 Cookie 条目（domain → cookie），给用户一个登录态的直观依据
  const cookieEntries = Object.entries(info?.cookies ?? {})

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div
        className="flex max-h-[88vh] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)]"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-3 border-b border-[var(--app-border)] px-4 py-3">
          <KeyRound size={16} className="shrink-0 text-brand-600" />
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-bold text-[var(--app-text)]">{sourceName}</p>
            <p className="text-2xs text-[var(--app-muted)]">
              {info?.logged_in ? '已登录' : '未登录'}
              {info?.has_login_js ? ' · 含登录逻辑' : ' · 未配置登录逻辑'}
            </p>
          </div>
          <button type="button" onClick={() => void load()} title="刷新" className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)]">
            <RefreshCw size={15} />
          </button>
          <button type="button" onClick={onClose} className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)]">
            <X size={16} />
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
          {loading && (
            <div className="flex items-center justify-center py-16 text-[var(--app-muted)]">
              <Loader2 className="animate-spin" size={20} />
            </div>
          )}

          {!loading && (
            <>
              {error && (
                <div className="mb-3 rounded-xl border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-500">
                  {error}
                </div>
              )}

              {/* 没有 loginUi 的表单时，只有 loginUrl：按 legado 语义当作登录页地址 */}
              {!info?.fields.length && info?.has_login_js && (
                <div className="mb-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-3 text-xs text-[var(--app-muted)]">
                  该书源没有表单（loginUi），登录需要打开网页完成。
                  <button type="button" onClick={() => openExternal(info.login_js ?? '')} className="btn-outline ml-2 text-2xs">
                    <ExternalLink size={12} className="mr-1 inline" />
                    打开登录页
                  </button>
                </div>
              )}

              {/* 表单输入项 */}
              {inputs.length > 0 && (
                <div className="space-y-2">
                  {inputs.map((f) => (
                    <label key={f.name} className="block">
                      <span className="mb-1 block text-2xs font-bold text-[var(--app-muted)]">{f.name}</span>
                      <input
                        type={f.type === 'password' ? 'password' : 'text'}
                        value={form[f.name] ?? ''}
                        onChange={(e) => setForm((prev) => ({ ...prev, [f.name]: e.target.value }))}
                        className="w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-xs text-[var(--app-text)] outline-none"
                      />
                    </label>
                  ))}
                </div>
              )}

              {/* 选择项（toggle / select）：点击后按 action 交由书源处理 */}
              {selects.length > 0 && (
                <div className="mt-3 space-y-2">
                  {selects.map((f) => (
                    <SelectField
                      key={f.name}
                      field={f}
                      value={form[f.name] ?? f.default ?? ''}
                      disabled={!!running}
                      onPick={(v) => {
                        setForm((prev) => ({ ...prev, [f.name]: v }))
                        if (f.action) void runAction(f.action)
                      }}
                    />
                  ))}
                </div>
              )}

              {/* 按钮区 */}
              {buttons.length > 0 && (
                <div className="mt-4 flex flex-wrap gap-2">
                  {buttons.map((f) => (
                    <button
                      key={`${f.name}-${f.action}`}
                      type="button"
                      disabled={!!running}
                      onClick={() => void runAction(f.action ?? '')}
                      className="btn-outline text-xs disabled:opacity-50"
                    >
                      {running === (f.action || '__login__') && <Loader2 size={12} className="mr-1 inline animate-spin" />}
                      {f.viewName || f.name}
                    </button>
                  ))}
                </div>
              )}

              {/* 提示与待打开地址 */}
              {toasts.length > 0 && (
                <pre className="mt-3 max-h-48 overflow-auto whitespace-pre-wrap rounded-xl bg-[var(--app-panel-soft)] p-3 font-mono text-2xs leading-5 text-[var(--app-text)]">
                  {toasts.filter(Boolean).join('\n')}
                </pre>
              )}

              {/* 源变量编辑 */}
              <div className="mt-4 border-t border-[var(--app-border)] pt-3">
                <button
                  type="button"
                  onClick={() => setShowVariable((v) => !v)}
                  className="text-2xs font-bold text-[var(--app-muted)] hover:text-brand-600"
                >
                  {showVariable ? '▾' : '▸'} 源变量（bookSourceUrl 变量的 JSON）
                </button>
                {showVariable && (
                  <div className="mt-2">
                    {info?.variable_comment && (
                      <p className="mb-2 whitespace-pre-wrap rounded-xl bg-[var(--app-panel-soft)] p-2 text-2xs text-[var(--app-muted)]">
                        {info.variable_comment}
                      </p>
                    )}
                    <textarea
                      value={variable}
                      onChange={(e) => setVariable(e.target.value)}
                      rows={6}
                      placeholder='{"线路":"https://v1.example.com"}'
                      className="w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-3 font-mono text-2xs text-[var(--app-text)] outline-none"
                    />
                    <button type="button" onClick={() => void saveVariable()} className="btn-primary mt-2 text-2xs">
                      <Save size={12} className="mr-1 inline" /> 保存变量
                    </button>
                  </div>
                )}
              </div>

              {/* 当前 Cookie：登录态的实际凭证 */}
              {cookieEntries.length > 0 && (
                <div className="mt-3 border-t border-[var(--app-border)] pt-3">
                  <p className="text-2xs font-bold text-[var(--app-muted)]">已保存 Cookie</p>
                  <div className="mt-1 space-y-1">
                    {cookieEntries.map(([domain, cookie]) => (
                      <p key={domain} className="truncate font-mono text-2xs text-[var(--app-muted)]" title={cookie}>
                        {domain}: {cookie}
                      </p>
                    ))}
                  </div>
                </div>
              )}

              {/* loginUrl 原文（书源作者写的登录脚本） */}
              {info?.login_js && (
                <div className="mt-3 border-t border-[var(--app-border)] pt-3">
                  <button
                    type="button"
                    onClick={() => setShowLoginJS((v) => !v)}
                    className="text-2xs font-bold text-[var(--app-muted)] hover:text-brand-600"
                  >
                    {showLoginJS ? '▾' : '▸'} 登录脚本（loginUrl）
                  </button>
                  {showLoginJS && (
                    <>
                      <button type="button" onClick={() => openExternal(info.login_js ?? '')} className="mt-2 mr-2 text-2xs text-brand-600">
                        <ExternalLink size={11} className="mr-1 inline" />
                        若为登录页地址可打开
                      </button>
                      <pre className="mt-2 max-h-64 overflow-auto rounded-xl bg-[var(--app-panel-soft)] p-3 font-mono text-2xs text-[var(--app-text)]">
                        {info.login_js}
                      </pre>
                    </>
                  )}
                </div>
              )}
            </>
          )}
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-[var(--app-border)] px-4 py-3">
          <button type="button" onClick={() => void saveLoginInfo()} className="btn-outline text-xs" disabled={loading}>
            <Save size={13} className="mr-1 inline" /> 仅保存信息
          </button>
          <button type="button" onClick={() => void doLogout()} className="btn-outline text-xs text-red-500" disabled={loading}>
            <LogOut size={13} className="mr-1 inline" /> 退出登录
          </button>
          <button type="button" onClick={onClose} className="btn-primary text-xs">
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}

// SelectField 渲染 toggle / select 控件。
function SelectField({
  field,
  value,
  disabled,
  onPick,
}: {
  field: ReaderLoginField
  value: string
  disabled: boolean
  onPick: (v: string) => void
}) {
  const options = field.chars ?? []
  if (field.type === 'toggle' || options.length === 0) {
    return (
      <div className="flex items-center gap-2">
        <span className="text-2xs text-[var(--app-muted)]">{field.name}</span>
        <button type="button" disabled={disabled} onClick={() => onPick(value ? '' : '1')} className="btn-outline text-2xs">
          {value ? '已开启' : '未开启'}
        </button>
      </div>
    )
  }
  return (
    <label className="block">
      <span className="mb-1 block text-2xs font-bold text-[var(--app-muted)]">{field.name}</span>
      <select
        value={value}
        disabled={disabled}
        onChange={(e) => onPick(e.target.value)}
        className="w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-xs text-[var(--app-text)] outline-none"
      >
        {options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    </label>
  )
}
