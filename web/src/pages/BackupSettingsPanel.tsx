import { useCallback, useEffect, useState } from 'react'
import { Archive, Download, LoaderCircle, RefreshCw, RotateCcw, Trash2 } from 'lucide-react'
import toast from 'react-hot-toast'

import { backupAPI, type BackupInfo } from '../api/backup'
import { adminAPI } from '../api/admin'
import { confirmAction } from '../components/confirmAction'

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

export function BackupSettingsPanel() {
  const [items, setItems] = useState<BackupInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [creating, setCreating] = useState(false)
  const [autoEnabled, setAutoEnabled] = useState(false)
  const [autoTime, setAutoTime] = useState('03:00')
  const [retention, setRetention] = useState('7')
  const [savingPolicy, setSavingPolicy] = useState(false)

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      setItems(await backupAPI.list())
    } catch {
      toast.error('读取备份列表失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  useEffect(() => {
    void adminAPI.listSettings().then((settings) => {
      const values = new Map(settings.map((setting) => [setting.key, setting.value]))
      setAutoEnabled(['true', '1'].includes(values.get('backup.auto_enabled') ?? 'false'))
      setAutoTime(values.get('backup.auto_time') || '03:00')
      setRetention(values.get('backup.retention') || '7')
    }).catch(() => toast.error('读取自动备份设置失败'))
  }, [])

  const savePolicy = async () => {
    const count = Number(retention)
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(autoTime) || !Number.isInteger(count) || count < 1 || count > 365) {
      toast.error('请填写有效时间，并将保留份数设为 1 到 365')
      return
    }
    setSavingPolicy(true)
    try {
      await adminAPI.updateSetting('backup.auto_time', autoTime)
      await adminAPI.updateSetting('backup.retention', String(count))
      await adminAPI.updateSetting('backup.auto_enabled', String(autoEnabled))
      toast.success('自动备份设置已保存')
    } catch {
      toast.error('保存自动备份设置失败')
    } finally {
      setSavingPolicy(false)
    }
  }

  const create = async () => {
    setCreating(true)
    try {
      const item = await backupAPI.create()
      toast.success(`备份已创建：${item.filename}`)
      await refresh()
    } catch {
      toast.error('创建备份失败，请查看服务日志')
    } finally {
      setCreating(false)
    }
  }

  const remove = async (item: BackupInfo) => {
    if (!await confirmAction({ title: '删除备份', message: `确定删除 ${item.filename}？此操作无法撤销。` })) return
    try {
      await backupAPI.remove(item.filename)
      setItems((current) => current.filter((entry) => entry.filename !== item.filename))
      toast.success('备份已删除')
    } catch {
      toast.error('删除备份失败')
    }
  }

  const restore = async (item: BackupInfo) => {
    const ok = await confirmAction({
      title: '恢复数据库备份',
      message: `将恢复 ${item.filename}。SQLite 会先校验并暂存备份，在重启时应用；PostgreSQL 会立即事务恢复。请先保存当前数据库备份，并在操作后重启 MeBox。`,
      confirmText: '恢复备份',
      danger: true,
    })
    if (!ok) return
    try {
      const result = await backupAPI.restore(item.filename)
      toast.success(result.message, { duration: 8000 })
    } catch (error) {
      const message = (error as { response?: { data?: { error?: string } } })?.response?.data?.error
      toast.error(message || '恢复失败，请查看服务日志')
    }
  }

  return (
    <section className="glass-panel space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <Archive className="text-brand-500" size={20} />
          <div>
            <h2 className="font-display text-xl font-bold text-ink-600">数据库备份</h2>
            <p className="text-xs text-ink-50">备份保存在服务器数据目录的 backups 文件夹中；请另行复制到其他设备。</p>
          </div>
        </div>
        <div className="flex gap-2">
          <button type="button" className="btn-outline" onClick={() => void refresh()} disabled={loading}><RefreshCw size={14} />刷新</button>
          <button type="button" className="btn-primary" onClick={() => void create()} disabled={creating}>
            {creating ? <LoaderCircle className="animate-spin" size={14} /> : <Archive size={14} />}立即备份
          </button>
        </div>
      </div>
      <div className="grid gap-3 rounded-xl border border-sand-200 p-4 sm:grid-cols-[1fr_auto_auto_auto] sm:items-end">
        <label className="flex items-center gap-2 text-sm font-semibold text-ink-600">
          <input type="checkbox" checked={autoEnabled} onChange={(event) => setAutoEnabled(event.target.checked)} />
          每日自动备份
        </label>
        <label className="space-y-1 text-xs text-ink-50">执行时间
          <input type="time" value={autoTime} onChange={(event) => setAutoTime(event.target.value)} className="block rounded-lg border border-sand-300 bg-white px-3 py-2 text-sm text-ink-600" />
        </label>
        <label className="space-y-1 text-xs text-ink-50">自动备份保留份数
          <input type="number" min={1} max={365} value={retention} onChange={(event) => setRetention(event.target.value)} className="block w-28 rounded-lg border border-sand-300 bg-white px-3 py-2 text-sm text-ink-600" />
        </label>
        <button type="button" className="btn-outline" onClick={() => void savePolicy()} disabled={savingPolicy}>{savingPolicy ? '保存中…' : '保存策略'}</button>
      </div>
      {loading ? <div className="py-8 text-center text-sm text-ink-50">正在读取备份…</div> : items.length === 0 ? (
        <div className="rounded-xl border border-dashed border-sand-300 px-4 py-8 text-center text-sm text-ink-50">还没有数据库备份</div>
      ) : (
        <div className="divide-y divide-sand-200">
          {items.map((item) => (
            <div key={item.filename} className="flex flex-wrap items-center justify-between gap-3 py-3">
              <div className="min-w-0">
                <div className="break-all text-sm font-semibold text-ink-600">{item.filename}</div>
                <div className="mt-1 text-xs text-ink-50">{item.automatic ? '自动备份' : '手动备份'} · {new Date(item.created_at).toLocaleString()} · {formatBytes(item.size)}</div>
              </div>
              <div className="flex flex-wrap gap-2">
                <button type="button" className="btn-outline" onClick={() => void backupAPI.download(item.filename).catch(() => toast.error('下载备份失败'))}><Download size={14} />下载</button>
                <button type="button" className="btn-outline" onClick={() => void restore(item)}><RotateCcw size={14} />恢复</button>
                <button type="button" className="btn-outline text-red-500" onClick={() => void remove(item)}><Trash2 size={14} />删除</button>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
