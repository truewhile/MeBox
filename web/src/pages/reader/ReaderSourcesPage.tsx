import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import toast from 'react-hot-toast'
import { ArrowLeft, Bug, Download, Loader2, Play, Trash2 } from 'lucide-react'

import { readerAPI, type ReaderSource } from '../../api/reader'
import { READER_SOURCE_TYPES } from './sourceTypes'

// 书源管理页（仿 legado 书源列表：启停开关、快速调试、导入）。

function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

export default function ReaderSourcesPage() {
  const navigate = useNavigate()
  const [sources, setSources] = useState<ReaderSource[] | null>(null)
  const [importText, setImportText] = useState('')
  const [importing, setImporting] = useState(false)
  const [showImport, setShowImport] = useState(false)
  const [debugId, setDebugId] = useState('')
  const [debugKey, setDebugKey] = useState('')
  const [debugLogs, setDebugLogs] = useState<string[] | null>(null)
  const [debugging, setDebugging] = useState(false)

  const load = () => {
    readerAPI
      .listSources()
      .then(setSources)
      .catch(() => toast.error('加载书源失败'))
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, [])

  const doImport = async () => {
    if (!importText.trim() || importing) return
    setImporting(true)
    try {
      const imported = await readerAPI.importSources(importText)
      toast.success(`成功导入 ${imported} 个书源`)
      setImportText('')
      setShowImport(false)
      load()
    } catch (e) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '导入失败'
      toast.error(msg)
    } finally {
      setImporting(false)
    }
  }

  const toggleEnabled = async (src: ReaderSource) => {
    try {
      await readerAPI.setSourceEnabled(src.id, !src.enabled)
      setSources((prev) => prev?.map((s) => (s.id === src.id ? { ...s, enabled: !s.enabled } : s)) ?? null)
    } catch {
      toast.error('更新失败')
    }
  }

  const removeSource = async (src: ReaderSource) => {
    if (!window.confirm(`删除书源「${src.name}」？`)) return
    try {
      await readerAPI.deleteSource(src.id)
      setSources((prev) => prev?.filter((s) => s.id !== src.id) ?? null)
      toast.success('已删除')
    } catch {
      toast.error('删除失败')
    }
  }

  const doDebug = async (src: ReaderSource) => {
    if (!debugKey.trim()) {
      toast.error('先填一个搜索关键词')
      return
    }
    setDebugging(true)
    setDebugLogs(null)
    try {
      const logs = await readerAPI.debugSource(src.id, debugKey.trim())
      setDebugLogs(logs)
    } catch (e) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '调试失败'
      toast.error(msg)
    } finally {
      setDebugging(false)
    }
  }

  return (
    <div className="mx-auto min-h-[100dvh] w-full max-w-4xl px-4 pb-16 pt-4 sm:px-6">
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={() => navigate(-1)}
          className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
        >
          <ArrowLeft size={18} />
        </button>
        <h1 className="flex-1 font-display text-lg text-ink-600">书源管理</h1>
        <button type="button" onClick={() => setShowImport((v) => !v)} className="btn-primary text-xs">
          <Download size={13} className="mr-1 inline" /> 导入书源
        </button>
      </div>

      {showImport && (
        <div className="mt-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4">
          <textarea
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
            rows={6}
            placeholder={'粘贴书源 JSON / Base64，或填一个书源链接（https://...）\n支持数组批量导入'}
            className="w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-3 text-xs text-[var(--app-text)] outline-none placeholder:text-[var(--app-muted)]"
          />
          <div className="mt-2 flex justify-end">
            <button type="button" onClick={doImport} disabled={importing || !importText.trim()} className="btn-primary text-xs disabled:opacity-50">
              {importing ? <Loader2 size={13} className="inline animate-spin" /> : '开始导入'}
            </button>
          </div>
        </div>
      )}

      <div className="mt-6 space-y-2">
        {sources === null && (
          <div className="flex items-center justify-center py-24 text-[var(--app-muted)]">
            <Loader2 className="animate-spin" size={22} />
          </div>
        )}
        {sources !== null && sources.length === 0 && (
          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-12 text-center text-xs text-[var(--app-muted)]">
            还没有书源，点右上「导入书源」开始
          </div>
        )}
        {sources?.map((src) => (
          <div key={src.id} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)]">
            <div className="flex items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <p className="flex items-center gap-2 truncate text-sm font-bold text-[var(--app-text)]">
                  {src.name}
                  <span className="shrink-0 rounded-md bg-brand-500/10 px-1.5 py-0.5 text-2xs font-bold text-brand-600">
                    {READER_SOURCE_TYPES[src.type] ?? '文本'}
                  </span>
                  {src.group && <span className="truncate text-2xs font-normal text-[var(--app-muted)]">{src.group}</span>}
                </p>
                <p className="mt-0.5 truncate text-xs text-[var(--app-muted)]">{hostOf(src.source_url)}</p>
              </div>
              <button
                type="button"
                title="快速调试"
                onClick={() => {
                  setDebugId(debugId === src.id ? '' : src.id)
                  setDebugLogs(null)
                }}
                className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-brand-600"
              >
                <Bug size={16} />
              </button>
              <button
                type="button"
                title="删除"
                onClick={() => removeSource(src)}
                className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-red-500"
              >
                <Trash2 size={16} />
              </button>
              {/* 启停开关（仿 legado Switch） */}
              <button
                type="button"
                role="switch"
                aria-checked={src.enabled}
                onClick={() => toggleEnabled(src)}
                className={`relative h-5 w-9 shrink-0 rounded-full transition ${
                  src.enabled ? 'bg-brand-500' : 'bg-[var(--app-hover)]'
                }`}
              >
                <span
                  className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all ${
                    src.enabled ? 'left-[18px]' : 'left-0.5'
                  }`}
                />
              </button>
            </div>

            {debugId === src.id && (
              <div className="border-t border-[var(--app-border)] px-4 py-3">
                <div className="flex items-center gap-2">
                  <input
                    value={debugKey}
                    onChange={(e) => setDebugKey(e.target.value)}
                    onKeyDown={(e) => e.key === 'Enter' && doDebug(src)}
                    placeholder="搜索关键词（如：斗破苍穹）"
                    className="flex-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-1.5 text-xs text-[var(--app-text)] outline-none"
                  />
                  <button type="button" onClick={() => doDebug(src)} disabled={debugging} className="btn-outline text-2xs disabled:opacity-50">
                    {debugging ? <Loader2 size={12} className="inline animate-spin" /> : <Play size={12} className="inline" />}
                    运行
                  </button>
                </div>
                {debugLogs !== null && (
                  <pre className="mt-3 max-h-72 overflow-auto rounded-xl bg-[var(--app-panel-soft)] p-3 font-mono text-2xs leading-5 text-[var(--app-text)]">
                    {debugLogs.length === 0 ? '（无日志）' : debugLogs.join('\n')}
                  </pre>
                )}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
