import { FormEvent, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Loader2, Save, SettingsIcon } from 'lucide-react'
import toast from 'react-hot-toast'

import { adminAPI } from '../api/admin'
import { libraryAPI } from '../api/library'
import type { Library, Setting } from '../types'
import { APIConfigsPanel } from '../components/APIConfigsPanel'
import { FFToolsPanel } from '../components/FFToolsPanel'
import { AboutSettingsPanel } from './AboutSettingsPanel'
import { AdultSettingsPanel } from './AdultSettingsPanel'
import { DatabaseSettingsPanel } from './DatabaseSettingsPanel'
import { RecognitionWordsPanel } from './RecognitionWordsPanel'
import { SettingRow } from './SettingsRow'
import { TelegramNotifyPanel } from './TelegramNotifyPanel'
import { ALL_KEYS, GROUPS } from './settingsGroups'

export function SettingsPage() {
  const [searchParams] = useSearchParams()
  const [activeGroup, setActiveGroup] = useState(() => {
    const fromQuery = searchParams.get('group')
    return GROUPS.some((g) => g.key === fromQuery) ? (fromQuery as string) : GROUPS[0].key
  })
  const [values, setValues] = useState<Record<string, string>>({})
  const [dirty, setDirty] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [libraries, setLibraries] = useState<Library[]>([])

  useEffect(() => {
    const fromQuery = searchParams.get('group')
    if (fromQuery && GROUPS.some((g) => g.key === fromQuery)) {
      setActiveGroup(fromQuery)
    }
  }, [searchParams])

  const refresh = async () => {
    setLoading(true)
    try {
      const [all, libs] = await Promise.all([
        adminAPI.listSettings(),
        libraryAPI.list({ includeHidden: true }).catch(() => [] as Library[]),
      ])
      const idx: Record<string, string> = {}
      for (const s of all as Setting[]) {
        if (ALL_KEYS.has(s.key)) idx[s.key] = s.value
      }
      setValues(idx)
      setLibraries(libs as Library[])
      setDirty(new Set())
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    refresh().catch(() => undefined)
  }, [])

  const onChange = (key: string, value: string) => {
    setValues((v) => ({ ...v, [key]: value }))
    setDirty((d) => new Set(d).add(key))
  }

  const onSave = async (e: FormEvent) => {
    e.preventDefault()
    if (dirty.size === 0) return

    const wantHTTPS = values['https.enabled'] === 'true' || values['https.enabled'] === '1'
    // 证书/私钥任一来源可用即可：路径优先，其次粘贴的内容。
    const materialOK = (content?: string, path?: string) =>
      Boolean((path ?? '').trim() || (content ?? '').trim())
    if (wantHTTPS && !(materialOK(values['https.cert'], values['https.cert_path']) &&
                       materialOK(values['https.key'], values['https.key_path']))) {
      toast.error('启用 HTTPS 前请先填写 SSL 证书和私钥（内容或路径任选其一）')
      return
    }

    setSaving(true)
    try {
      // 证书/私钥（内容与路径）先保存、启用开关最后保存，后端校验开关时才能读到最新的配置。
      const rank = (k: string) =>
        k === 'https.cert' ||
        k === 'https.key' ||
        k === 'https.cert_path' ||
        k === 'https.key_path'
          ? 0
          : k === 'https.enabled'
            ? 2
            : 1
      const orderedKeys = [...dirty].sort((a, b) => rank(a) - rank(b))

      const failures: string[] = []
      for (const key of orderedKeys) {
        try {
          await adminAPI.updateSetting(key, values[key] ?? '')
        } catch (err) {
          failures.push(
            (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
              `保存 ${key} 失败`,
          )
        }
      }
      if (failures.length > 0) {
        toast.error(failures[0])
        return
      }

      toast.success(`已保存 ${dirty.size} 项配置`)
      setDirty(new Set())

      // 切换 HTTPS 后（或关闭 HTTPS 后）连接会短暂中断，自动跳转到对应协议地址。
      const currentIsHTTPS = window.location.protocol === 'https:'
      const targetProto = wantHTTPS ? 'https:' : 'http:'
      if (wantHTTPS !== currentIsHTTPS) {
        toast(wantHTTPS ? '正在切换到 HTTPS 访问…' : '正在切换回 HTTP 访问…')
        window.setTimeout(() => {
          const url =
            targetProto +
            '//' +
            window.location.host +
            window.location.pathname +
            window.location.search
          window.location.replace(url)
        }, 800)
      }
    } finally {
      setSaving(false)
    }
  }

  const group = GROUPS.find((g) => g.key === activeGroup)!

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-sand-300/40 text-ink-100">
          <SettingsIcon size={20} />
        </div>
        <div>
          <h1 className="font-display text-3xl font-bold text-ink-600">系统设置</h1>
          <p className="text-sm text-ink-50">
            按分组编辑转码 / 网盘转存 / Adult 等关键配置
          </p>
        </div>
      </div>

      <div className="flex gap-2 overflow-x-auto border-b border-gray-200">
        {GROUPS.map((g) => (
          <button
            key={g.key}
            onClick={() => setActiveGroup(g.key)}
            className={
              'border-b-2 px-4 py-2 text-sm whitespace-nowrap transition ' +
              (activeGroup === g.key
                ? 'border-primary-400 text-brand-500'
                : 'border-transparent text-ink-50 hover:text-white')
            }
          >
            {g.label}
          </button>
        ))}
      </div>

      {loading && (
        <div className="flex justify-center py-12 text-ink-50">
          <Loader2 className="animate-spin" />
        </div>
      )}

      {!loading && (
        <div className="space-y-4">
          {group.key === 'database' && <DatabaseSettingsPanel />}
          {group.key === 'api-configs' && <APIConfigsPanel />}
          {group.key === 'recognition-words' && <RecognitionWordsPanel />}
          {group.key === 'adult' && <AdultSettingsPanel />}
          {group.key === 'general' && <FFToolsPanel onInstalled={() => refresh().catch(() => undefined)} />}
          {group.key === 'about' && <AboutSettingsPanel />}
          {group.key === 'device-notify' && <TelegramNotifyPanel />}
          {group.key !== 'adult' && group.key !== 'library' && group.items.length > 0 && (
            <form onSubmit={onSave} className="glass-panel space-y-4">
              {group.description && <p className="text-xs text-sand-500">{group.description}</p>}
              {group.items.map((it) => (
                <SettingRow
                  key={it.key}
                  def={it}
                  value={values[it.key] ?? it.defaultValue ?? ''}
                  onChange={(v) => onChange(it.key, v)}
                  libraries={libraries}
                />
              ))}
              <div className="flex items-center justify-between pt-2">
                <span className="text-xs text-sand-500">
                  {dirty.size > 0 ? `有 ${dirty.size} 项未保存` : '所有更改已保存'}
                </span>
                <button
                  type="submit"
                  disabled={saving || dirty.size === 0}
                  className="neon-button disabled:opacity-50"
                >
                  {saving ? <Loader2 size={16} className="animate-spin" /> : <Save size={16} />}
                  保存
                </button>
              </div>
            </form>
          )}
        </div>
      )}
    </div>
  )
}
