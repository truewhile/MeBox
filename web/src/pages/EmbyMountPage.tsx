import { useCallback, useEffect, useState } from 'react'
import toast from 'react-hot-toast'
import {
  ArrowDown,
  ArrowUp,
  Check,
  Globe,
  GripVertical,
  Loader2,
  Pencil,
  Plus,
  Power,
  PowerOff,
  RefreshCw,
  Server,
  Trash2,
  Tv,
  X,
} from 'lucide-react'

import { strmAPI } from '../api/strm'
import { embyAPI } from '../api/emby'
import type { StrmAccount } from '../types/strm'
import type { EmbyMount, RemoteEmbyView } from '../types/emby'
import { apiErrorMessage } from './StrmManagePage'
import { confirmAction } from '../components/confirmAction'
import {
  defaultEmbyRemoteLines,
  encodeEmbyRemoteConfigLines,
  normalizeEmbyRemoteLines,
  type EmbyRemoteLine,
} from '../utils/embyRemoteLines'

const inputCls = 'input-base w-full'

// ─── 账号对话框（Emby 服务器连接） ─────────────────────────────────────────────

function AccountDialog({
  existing,
  onClose,
  onSaved,
}: {
  existing: StrmAccount | null
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(existing?.name ?? '')
  const [lines, setLines] = useState<EmbyRemoteLine[]>(() => defaultEmbyRemoteLines(existing?.emby_lines))
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [token, setToken] = useState('')
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [saving, setSaving] = useState(false)

  const normalizedLines = normalizeEmbyRemoteLines(lines)
  const canSave = () => normalizedLines.length > 0 && (token !== '' || password !== '' || !!existing?.has_credential)

  const updateLine = (index: number, patch: Partial<EmbyRemoteLine>) => {
    setLines((prev) => prev.map((line, i) => (i === index ? { ...line, ...patch } : line)))
  }

  const addLine = () => {
    setLines((prev) => [...prev, { name: `线路 ${prev.length + 1}`, url: '' }])
  }

  const removeLine = (index: number) => {
    setLines((prev) => (prev.length <= 1 ? prev : prev.filter((_, i) => i !== index)))
  }

  const moveLine = (index: number, direction: 'up' | 'down') => {
    setLines((prev) => {
      const target = direction === 'up' ? index - 1 : index + 1
      if (target < 0 || target >= prev.length) return prev
      const next = [...prev]
      const [moved] = next.splice(index, 1)
      next.splice(target, 0, moved)
      return next
    })
  }

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    const readyLines = normalizeEmbyRemoteLines(lines)
    if (readyLines.length === 0) {
      toast.error('请至少填写一条有效的 Emby 线路地址')
      return
    }
    setSaving(true)
    try {
      const config: Record<string, string> = {
        ...encodeEmbyRemoteConfigLines(readyLines),
        ...(username ? { username } : {}),
        ...(password ? { password } : {}),
        ...(token ? { token } : {}),
      }
      if (existing) {
        await strmAPI.updateAccount(existing.id, {
          name: name || existing.name,
          provider: existing.provider,
          enabled,
          config: Object.keys(config).length ? config : {},
        })
        toast.success('Emby 账号已更新')
      } else {
        if (!canSave()) {
          toast.error('请填写线路地址与密码（或 API Key）')
          return
        }
        await strmAPI.createAccount({ name: name || 'Emby 服务器', provider: 'emby_remote', config })
        toast.success('Emby 账号已添加')
      }
      onSaved()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className="max-h-[90vh] w-full max-w-lg overflow-y-auto rounded-3xl border border-sand-200 bg-white p-6 shadow-2xl" onClick={(e) => e.stopPropagation()}>
        <div className="mb-5 flex items-center justify-between">
          <div>
            <h3 className="font-display text-xl font-bold text-ink-600">{existing ? '编辑 Emby 账号' : '添加 Emby 账号'}</h3>
            <p className="text-xs text-sand-500">可配置内网、外网等多条线路，连接失败时按顺序自动切换</p>
          </div>
          <button onClick={onClose} className="rounded-lg p-1.5 text-sand-500 hover:bg-gray-100">
            <X size={18} />
          </button>
        </div>
        <form onSubmit={submit} className="space-y-4">
          <label className="block text-sm">
            <span className="mb-1 block font-semibold text-ink-600">账号名称</span>
            <input className={inputCls} value={name} placeholder="例如：家庭影院" onChange={(e) => setName(e.target.value)} />
          </label>
          <div className="space-y-3">
            <div className="flex items-center justify-between gap-2">
              <span className="text-sm font-semibold text-ink-600">接入线路</span>
              <button type="button" onClick={addLine} className="inline-flex items-center gap-1 rounded-lg border border-gray-200 px-2.5 py-1 text-xs font-semibold text-ink-100 hover:bg-gray-50">
                <Plus size={12} />
                添加线路
              </button>
            </div>
            {lines.map((line, index) => (
              <div key={`line-${index}`} className="space-y-2 rounded-2xl border border-sand-200 bg-gray-50/70 p-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-bold uppercase tracking-wide text-sand-500">
                    线路 {index + 1}
                    {existing?.emby_active_line === index ? ' · 当前优先' : index === 0 ? ' · 默认优先' : ''}
                  </span>
                  <div className="flex items-center gap-1">
                    <button type="button" onClick={() => moveLine(index, 'up')} disabled={index === 0} className="rounded-lg border border-gray-200 p-1 text-ink-100 hover:bg-white disabled:opacity-30">
                      <ArrowUp size={12} />
                    </button>
                    <button type="button" onClick={() => moveLine(index, 'down')} disabled={index === lines.length - 1} className="rounded-lg border border-gray-200 p-1 text-ink-100 hover:bg-white disabled:opacity-30">
                      <ArrowDown size={12} />
                    </button>
                    <button type="button" onClick={() => removeLine(index)} disabled={lines.length <= 1} className="rounded-lg border border-gray-200 p-1 text-red-500 hover:bg-red-50 disabled:opacity-30">
                      <Trash2 size={12} />
                    </button>
                  </div>
                </div>
                <input
                  className={inputCls}
                  value={line.name}
                  placeholder="线路名称，如：内网 / 外网 / IPv6"
                  onChange={(e) => updateLine(index, { name: e.target.value })}
                />
                <input
                  className={inputCls}
                  value={line.url}
                  placeholder="http://192.168.1.10:8096 或 https://emby.example.com"
                  onChange={(e) => updateLine(index, { url: e.target.value })}
                />
              </div>
            ))}
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label className="block text-sm">
              <span className="mb-1 block font-semibold text-ink-600">用户名</span>
              <input className={inputCls} value={username} onChange={(e) => setUsername(e.target.value)} />
            </label>
            <label className="block text-sm">
              <span className="mb-1 block font-semibold text-ink-600">密码</span>
              <input className={inputCls} type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
          </div>
          <label className="block text-sm">
            <span className="mb-1 block font-semibold text-ink-600">API Key（可选）</span>
            <input
              className={inputCls}
              value={token}
              placeholder="留空则用用户名/密码自动认证"
              onChange={(e) => setToken(e.target.value)}
            />
          </label>
          {existing && (
            <label className="flex cursor-pointer items-center gap-2 text-sm text-ink-100">
              <input type="checkbox" className="h-4 w-4 accent-primary-400" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
              启用该账号
            </label>
          )}
          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={onClose} className="rounded-xl border border-gray-200 px-4 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50">
              取消
            </button>
            <button type="submit" disabled={saving} className="neon-button disabled:opacity-50">
              {saving ? <Loader2 size={16} className="animate-spin" /> : <Server size={16} />}
              保存
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

// ─── 添加挂载对话框 ────────────────────────────────────────────────────────────

function MountDialog({
  account,
  onClose,
  onSaved,
}: {
  account: StrmAccount
  onClose: () => void
  onSaved: () => void
}) {
  const [views, setViews] = useState<RemoteEmbyView[]>([])
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const [proxy, setProxy] = useState<Record<string, boolean>>({})
  const nameOverride: Record<string, string> = {}
  const [allProxy, setAllProxy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    embyAPI
      .listAccountViews(account.id)
      .then((rows) => {
        setViews(rows)
        const init: Record<string, boolean> = {}
        rows.forEach((v) => {
          if (!v.already_mounted) init[v.remote_view_id] = true
        })
        setSelected(init)
      })
      .catch((err) => toast.error(apiErrorMessage(err)))
      .finally(() => setLoading(false))
  }, [account.id])

  const toggleView = (id: string) => setSelected((prev) => ({ ...prev, [id]: !prev[id] }))
  const toggleProxy = (id: string) => setProxy((prev) => ({ ...prev, [id]: !prev[id] }))

  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    const picked = views.filter((v) => selected[v.remote_view_id])
    if (picked.length === 0) {
      toast.error('请至少选择一个媒体库')
      return
    }
    setSaving(true)
    try {
      await embyAPI.createMounts({
        account_id: account.id,
        views: picked.map((v) => ({
          remote_view_id: v.remote_view_id,
          remote_view_name: v.remote_view_name,
          collection_type: v.collection_type,
          name: nameOverride[v.remote_view_id] || '',
          proxy_play: allProxy || proxy[v.remote_view_id] || false,
        })),
      })
      toast.success(`已挂载 ${picked.length} 个媒体库`)
      onSaved()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className="flex max-h-[85vh] w-full max-w-2xl flex-col rounded-3xl border border-sand-200 bg-white p-6 shadow-2xl" onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex items-center justify-between">
          <div>
            <h3 className="font-display text-xl font-bold text-ink-600">选择要挂载的媒体库</h3>
            <p className="text-xs text-sand-500">
              「{account.name}」上的远程媒体库，勾选后出现在本项目媒体库；未勾选的不挂载
            </p>
          </div>
          <button onClick={onClose} className="rounded-lg p-1.5 text-sand-500 hover:bg-gray-100">
            <X size={18} />
          </button>
        </div>
        <div className="mb-3 flex items-center justify-between rounded-xl bg-gray-50 px-4 py-2.5 text-sm">
          <label className="flex items-center gap-2 font-semibold text-ink-600">
            <input type="checkbox" className="h-4 w-4 accent-primary-400" checked={allProxy} onChange={(e) => setAllProxy(e.target.checked)} />
            全部走本服务器代理播放
          </label>
          <span className="text-xs text-sand-500">关闭（默认）= 客户端直连远程 Emby 拉流</span>
        </div>
        <div className="min-h-0 flex-1 space-y-2 overflow-y-auto pr-1">
          {loading ? (
            <div className="flex items-center justify-center gap-2 py-10 text-sm text-sand-500">
              <Loader2 size={16} className="animate-spin" /> 正在读取远程媒体库…
            </div>
          ) : views.length === 0 ? (
            <p className="py-10 text-center text-sm text-sand-500">远程服务器没有可挂载的媒体库</p>
          ) : (
            views.map((v) => {
              const checked = selected[v.remote_view_id]
              const disabled = v.already_mounted
              return (
                <div key={v.remote_view_id} className={`flex items-center gap-3 rounded-xl border px-3.5 py-2.5 ${disabled ? 'border-gray-100 bg-gray-50 opacity-60' : 'border-sand-200 bg-white'}`}>
                  <input
                    type="checkbox"
                    className="h-4 w-4 accent-primary-400"
                    checked={disabled ? false : checked}
                    disabled={disabled}
                    onChange={() => toggleView(v.remote_view_id)}
                  />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-semibold text-ink-600">{v.remote_view_name}</p>
                    <p className="text-xs text-sand-500">
                      {v.collection_type || 'mixed'}
                      {v.child_count > 0 ? ` · ${v.child_count} 条目` : ''}
                      {disabled ? ' · 已挂载' : ''}
                    </p>
                  </div>
                  {!disabled && (
                    <label className="flex shrink-0 cursor-pointer items-center gap-1.5 text-xs font-semibold text-ink-100">
                      <input
                        type="checkbox"
                        className="h-3.5 w-3.5 accent-primary-400"
                        checked={proxy[v.remote_view_id] || false}
                        disabled={allProxy}
                        onChange={() => toggleProxy(v.remote_view_id)}
                      />
                      代理
                    </label>
                  )}
                </div>
              )
            })
          )}
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <button onClick={onClose} className="rounded-xl border border-gray-200 px-4 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50">
            取消
          </button>
          <button type="button" onClick={submit} disabled={saving || loading} className="neon-button disabled:opacity-50">
            {saving ? <Loader2 size={16} className="animate-spin" /> : <Tv size={16} />}
            挂载所选媒体库
          </button>
        </div>
      </div>
    </div>
  )
}

// ─── 页面主体 ──────────────────────────────────────────────────────────────────

export function EmbyMountPage() {
  const [accounts, setAccounts] = useState<StrmAccount[]>([])
  const [mounts, setMounts] = useState<EmbyMount[]>([])
  const [loading, setLoading] = useState(true)
  const [accountDialog, setAccountDialog] = useState<StrmAccount | null | 'new'>(null)
  const [mountDialogFor, setMountDialogFor] = useState<StrmAccount | null>(null)
  const [testingID, setTestingID] = useState<string | null>(null)
  const [draggingId, setDraggingId] = useState<string | null>(null)
  const [dragOverId, setDragOverId] = useState<string | null>(null)
  const [reordering, setReordering] = useState(false)

  const applyReorder = async (nextMounts: EmbyMount[]) => {
    const prevMounts = mounts
    const updated = nextMounts.map((m, idx) => ({ ...m, sort_order: idx }))
    setMounts(updated)
    setReordering(true)
    try {
      await embyAPI.reorderMounts(updated.map((m) => m.id))
      toast.success('媒体库顺序已更新')
    } catch (err) {
      setMounts(prevMounts)
      toast.error(apiErrorMessage(err))
    } finally {
      setReordering(false)
    }
  }

  const moveMount = (m: EmbyMount, direction: 'up' | 'down') => {
    const acctMounts = mounts.filter((item) => item.account_id === m.account_id)
    const idx = acctMounts.findIndex((item) => item.id === m.id)
    if (idx < 0) return
    if (direction === 'up' && idx === 0) return
    if (direction === 'down' && idx === acctMounts.length - 1) return

    const targetIdx = direction === 'up' ? idx - 1 : idx + 1
    const newAcctMounts = [...acctMounts]
    const [moved] = newAcctMounts.splice(idx, 1)
    newAcctMounts.splice(targetIdx, 0, moved)

    let acctPointer = 0
    const nextMounts = mounts.map((item) => {
      if (item.account_id === m.account_id) {
        return newAcctMounts[acctPointer++]
      }
      return item
    })
    applyReorder(nextMounts)
  }

  const handleDragStart = (e: React.DragEvent, id: string) => {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', id)
    setDraggingId(id)
  }

  const handleDragOver = (e: React.DragEvent, overId: string) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    if (dragOverId !== overId) {
      setDragOverId(overId)
    }
  }

  const handleDrop = (e: React.DragEvent, overId: string, accountId: string) => {
    e.preventDefault()
    const fromId = draggingId || e.dataTransfer.getData('text/plain')
    setDraggingId(null)
    setDragOverId(null)
    if (!fromId || fromId === overId) {
      return
    }
    const acctMounts = mounts.filter((item) => item.account_id === accountId)
    const fromIdx = acctMounts.findIndex((item) => item.id === fromId)
    const toIdx = acctMounts.findIndex((item) => item.id === overId)
    if (fromIdx < 0 || toIdx < 0) {
      return
    }
    const newAcctMounts = [...acctMounts]
    const [moved] = newAcctMounts.splice(fromIdx, 1)
    newAcctMounts.splice(toIdx, 0, moved)

    let acctPointer = 0
    const nextMounts = mounts.map((item) => {
      if (item.account_id === accountId) {
        return newAcctMounts[acctPointer++]
      }
      return item
    })
    applyReorder(nextMounts)
  }

  const load = useCallback(async () => {
    const [accts, mts] = await Promise.all([
      strmAPI.listAccounts().then((rows) => rows.filter((a) => a.provider === 'emby_remote')),
      embyAPI.listMounts(),
    ])
    setAccounts(accts)
    setMounts(mts)
  }, [])

  useEffect(() => {
    load()
      .catch((err) => toast.error(apiErrorMessage(err)))
      .finally(() => setLoading(false))
  }, [load])

  const testAccount = async (acct: StrmAccount) => {
    setTestingID(acct.id)
    try {
      const updated = await strmAPI.testAccount(acct.id)
      toast.success(updated.last_test_ok ? `「${updated.name}」连接正常` : `连接失败：${updated.last_test_result}`)
      await load()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setTestingID(null)
    }
  }

  const removeAccount = async (acct: StrmAccount) => {
    if (!(await confirmAction({ message: `删除 Emby 账号「${acct.name}」？其下 ${mounts.filter((m) => m.account_id === acct.id).length} 个挂载会一并移除。`, confirmText: '删除' }))) return
    try {
      await strmAPI.deleteAccount(acct.id)
      toast.success('账号已删除')
      await load()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  const fullMount = async (acct: StrmAccount) => {
    if (!(await confirmAction({ message: `把「${acct.name}」上的全部媒体库一键挂载（播放默认直连远程）？`, confirmText: '全量挂载' }))) return
    try {
      const res = await embyAPI.fullMountAccount(acct.id, false)
      toast.success(res.created > 0 ? `已挂载全部 ${res.created} 个媒体库` : '没有新的媒体库需要挂载')
      await load()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  const removeMount = async (mount: EmbyMount) => {
    if (!(await confirmAction({ message: `取消挂载「${mount.remote_view_name || mount.name}」？媒体库列表中将不再显示。`, confirmText: '取消挂载' }))) return
    try {
      await embyAPI.deleteMount(mount.id)
      await load()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  const toggleMount = async (mount: EmbyMount, enabled: boolean) => {
    try {
      await embyAPI.updateMount(mount.id, { enabled })
      await load()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  const toggleMountProxy = async (mount: EmbyMount, proxy: boolean) => {
    try {
      await embyAPI.updateMount(mount.id, { proxy_play: proxy })
      await load()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  return (
    <div className="space-y-8">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-bold text-ink-600">
            Emby 挂载
            <span className="text-sand-500">
              {' '}
              ({mounts.length} 个媒体库)
            </span>
          </h1>
          <p className="text-sm text-ink-50">
            添加远程 Emby 服务器后，按需挂载其媒体库到本项目；可为同一服务器配置多条线路并自动切换
          </p>
        </div>
        <button onClick={() => setAccountDialog('new')} className="neon-button">
          <Server size={16} />
          添加 Emby 账号
        </button>
      </div>

      {loading ? (
        <div className="flex items-center justify-center gap-2 py-20 text-sm text-sand-500">
          <Loader2 size={18} className="animate-spin" /> 加载中…
        </div>
      ) : accounts.length === 0 ? (
        <div className="rounded-3xl border border-dashed border-sand-300 bg-white/60 p-14 text-center">
          <Globe size={36} className="mx-auto mb-3 text-sand-400" />
          <p className="font-semibold text-ink-600">还没有 Emby 服务器</p>
          <p className="mt-1 text-sm text-sand-500">点击右上角「添加 Emby 账号」，填入服务器地址与凭据后即可挂载媒体库</p>
        </div>
      ) : (
        accounts.map((acct) => {
          const acctMounts = mounts.filter((m) => m.account_id === acct.id)
          return (
            <div key={acct.id} className="overflow-hidden rounded-3xl border border-sand-200 bg-white shadow-card">
              {/* 账号头部 */}
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-sand-100 bg-gradient-to-r from-brand-50/60 to-transparent px-5 py-4">
                <div className="flex items-center gap-3">
                  <span className="flex h-10 w-10 items-center justify-center rounded-2xl bg-brand-100 text-brand-600">
                    <Server size={20} />
                  </span>
                  <div>
                    <p className="font-display text-lg font-bold text-ink-600">{acct.name}</p>
                    <p className="text-xs text-sand-500">
                      {acct.has_credential ? '凭据已配置' : '待补全凭据'}
                      {acct.emby_lines && acct.emby_lines.length > 0
                        ? ` · ${acct.emby_lines.length} 条线路`
                        : ''}
                      {acct.last_test_result ? ` · 最近测试：${acct.last_test_ok ? '正常' : acct.last_test_result}` : ''}
                    </p>
                  </div>
                </div>
                <div className="flex flex-wrap gap-2">
                  <button onClick={() => testAccount(acct)} disabled={testingID === acct.id} className="iconButtonCls">
                    {testingID === acct.id ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}
                    测试
                  </button>
                  <button onClick={() => setAccountDialog(acct)} className="iconButtonCls">
                    <Pencil size={13} />
                    编辑
                  </button>
                  <button onClick={() => removeAccount(acct)} className="iconButtonCls !text-red-500 hover:!bg-red-50">
                    <Trash2 size={13} />
                    删除
                  </button>
                </div>
              </div>

              {/* 挂载列表 */}
              <div className="px-5 py-4">
                {acctMounts.length === 0 ? (
                  <p className="py-6 text-center text-sm text-sand-500">
                    尚未挂载媒体库 —— 点击下方「选择挂载」，或一键全量挂载该服务器的全部媒体库
                  </p>
                ) : (
                  <div className="space-y-2">
                    {acctMounts.map((m, index) => (
                      <div
                        key={m.id}
                        onDragOver={(e) => handleDragOver(e, m.id)}
                        onDrop={(e) => handleDrop(e, m.id, acct.id)}
                        className={`flex items-center gap-3 rounded-xl border px-3 py-3 transition-colors ${
                          draggingId === m.id
                            ? 'border-dashed border-brand-400 bg-brand-50/20 opacity-50'
                            : dragOverId === m.id
                              ? 'border-brand-400 bg-brand-50/30'
                              : m.enabled
                                ? 'border-sand-200 bg-gray-50/60'
                                : 'border-gray-100 bg-gray-50 opacity-60'
                        }`}
                      >
                        <div
                          draggable
                          onDragStart={(e) => handleDragStart(e, m.id)}
                          onDragEnd={() => {
                            setDraggingId(null)
                            setDragOverId(null)
                          }}
                          className="shrink-0 cursor-grab active:cursor-grabbing text-sand-400 hover:text-ink-600 p-0.5"
                          title="按住拖拽调整顺序"
                        >
                          <GripVertical size={16} />
                        </div>
                        <Tv size={16} className="shrink-0 text-brand-500" />
                        <div className="min-w-0 flex-1">
                          <p className="truncate text-sm font-semibold text-ink-600">
                            {m.name || m.remote_view_name || '未命名'}
                          </p>
                          <p className="text-xs text-sand-500">
                            {m.collection_type || 'mixed'}
                            {m.proxy_play ? ' · 本机代理播放' : ' · 直连播放'}
                          </p>
                        </div>
                        <div className="flex items-center gap-1">
                          <button
                            onClick={() => moveMount(m, 'up')}
                            disabled={index === 0 || reordering}
                            className="rounded-lg border border-gray-200 p-1.5 text-ink-100 hover:bg-gray-100 disabled:opacity-30 disabled:cursor-not-allowed"
                            title="上移"
                          >
                            <ArrowUp size={13} />
                          </button>
                          <button
                            onClick={() => moveMount(m, 'down')}
                            disabled={index === acctMounts.length - 1 || reordering}
                            className="rounded-lg border border-gray-200 p-1.5 text-ink-100 hover:bg-gray-100 disabled:opacity-30 disabled:cursor-not-allowed"
                            title="下移"
                          >
                            <ArrowDown size={13} />
                          </button>
                        </div>
                        <button
                          onClick={() => toggleMountProxy(m, !m.proxy_play)}
                          disabled={!m.enabled}
                          className="rounded-lg border border-gray-200 px-2.5 py-1.5 text-xs font-semibold text-ink-100 hover:bg-gray-100 disabled:opacity-40"
                          title="切换播放代理"
                        >
                          {m.proxy_play ? '代理中' : '直连'}
                        </button>
                        <button
                          onClick={() => toggleMount(m, !m.enabled)}
                          className="rounded-lg border border-gray-200 p-1.5 text-ink-100 hover:bg-gray-100"
                          title={m.enabled ? '停用挂载' : '启用挂载'}
                        >
                          {m.enabled ? <Power size={14} /> : <PowerOff size={14} />}
                        </button>
                        <button onClick={() => removeMount(m)} className="rounded-lg border border-gray-200 p-1.5 text-red-500 hover:bg-red-50" title="取消挂载">
                          <Trash2 size={14} />
                        </button>
                      </div>
                    ))}
                  </div>
                )}
                <div className="mt-4 flex flex-wrap gap-2">
                  <button onClick={() => setMountDialogFor(acct)} className="btn-outline">
                    <Plus size={14} />
                    选择挂载媒体库
                  </button>
                  <button onClick={() => fullMount(acct)} className="btn-outline">
                    <Check size={14} />
                    全量挂载
                  </button>
                </div>
              </div>
            </div>
          )
        })
      )}

      {accountDialog && (
        <AccountDialog existing={accountDialog === 'new' ? null : accountDialog} onClose={() => setAccountDialog(null)} onSaved={() => { setAccountDialog(null); load() }} />
      )}
      {mountDialogFor && <MountDialog account={mountDialogFor} onClose={() => setMountDialogFor(null)} onSaved={() => { setMountDialogFor(null); load() }} />}
    </div>
  )
}

const iconButtonCls =
  'inline-flex items-center gap-1 rounded-lg border border-gray-200 px-2 py-1 text-xs font-semibold text-ink-100 transition hover:bg-gray-50'

void iconButtonCls