import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import toast from 'react-hot-toast'
import { ArrowLeft, Loader2, Plus, Trash2 } from 'lucide-react'

import { readerAPI, type ReaderReplaceRule, type ReplaceRuleInput } from '../../api/reader'

// 替换净化规则页（仿 legado ReplaceRuleActivity：列表 + 启停 + 编辑）。

const emptyInput: ReplaceRuleInput = {
  name: '',
  group: '',
  pattern: '',
  replacement: '',
  scope: '',
  scope_title: false,
  scope_content: true,
  exclude_scope: '',
  is_enabled: true,
  is_regex: true,
  timeout_millisecond: 3000,
  order: 0,
}

export default function ReaderReplacePage() {
  const navigate = useNavigate()
  const [rules, setRules] = useState<ReaderReplaceRule[] | null>(null)
  const [showForm, setShowForm] = useState(false)
  const [form, setForm] = useState<ReplaceRuleInput>(emptyInput)
  const [saving, setSaving] = useState(false)

  const load = () => {
    readerAPI
      .listReplaceRules()
      .then(setRules)
      .catch(() => toast.error('加载替换规则失败'))
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, [])

  const save = async () => {
    if (!form.pattern.trim()) {
      toast.error('替换规则不能为空')
      return
    }
    setSaving(true)
    try {
      await readerAPI.createReplaceRule(form)
      toast.success('已添加')
      setForm(emptyInput)
      setShowForm(false)
      load()
    } catch (e) {
      toast.error((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '保存失败')
    } finally {
      setSaving(false)
    }
  }

  const toggle = async (r: ReaderReplaceRule) => {
    try {
      await readerAPI.updateReplaceRule(r.id, {
        name: r.name,
        group: r.group,
        pattern: r.pattern,
        replacement: r.replacement,
        scope: r.scope,
        scope_title: r.scope_title,
        scope_content: r.scope_content,
        exclude_scope: r.exclude_scope,
        is_enabled: !r.is_enabled,
        is_regex: r.is_regex,
        timeout_millisecond: 3000,
        order: r.order,
      })
      setRules((prev) => prev?.map((x) => (x.id === r.id ? { ...x, is_enabled: !x.is_enabled } : x)) ?? null)
    } catch {
      toast.error('更新失败')
    }
  }

  const remove = async (r: ReaderReplaceRule) => {
    if (!window.confirm(`删除规则「${r.name || r.pattern}」？`)) return
    try {
      await readerAPI.deleteReplaceRule(r.id)
      setRules((prev) => prev?.filter((x) => x.id !== r.id) ?? null)
    } catch {
      toast.error('删除失败')
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
        <h1 className="flex-1 font-display text-lg text-ink-600">替换净化</h1>
        <button type="button" onClick={() => setShowForm((v) => !v)} className="btn-primary text-xs">
          <Plus size={13} className="mr-1 inline" /> 添加规则
        </button>
      </div>

      {showForm && (
        <div className="mt-4 space-y-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder="规则名（可选）"
              className="rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-xs text-[var(--app-text)] outline-none"
            />
            <input
              value={form.group}
              onChange={(e) => setForm({ ...form, group: e.target.value })}
              placeholder="分组（可选）"
              className="rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-xs text-[var(--app-text)] outline-none"
            />
          </div>
          <textarea
            value={form.pattern}
            onChange={(e) => setForm({ ...form, pattern: e.target.value })}
            rows={2}
            placeholder="替换规则（正则或原文）"
            className="w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-3 text-xs text-[var(--app-text)] outline-none"
          />
          <textarea
            value={form.replacement}
            onChange={(e) => setForm({ ...form, replacement: e.target.value })}
            rows={2}
            placeholder="替换为（留空即删除匹配内容）"
            className="w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-3 text-xs text-[var(--app-text)] outline-none"
          />
          <div className="flex flex-wrap items-center gap-4 text-xs text-[var(--app-muted)]">
            <label className="flex items-center gap-1.5">
              <input type="checkbox" checked={form.is_regex} onChange={(e) => setForm({ ...form, is_regex: e.target.checked })} />
              正则
            </label>
            <label className="flex items-center gap-1.5">
              <input type="checkbox" checked={form.scope_content} onChange={(e) => setForm({ ...form, scope_content: e.target.checked })} />
              作用于正文
            </label>
            <input
              value={form.scope}
              onChange={(e) => setForm({ ...form, scope: e.target.value })}
              placeholder="作用范围（书名包含，可选）"
              className="w-44 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-1.5 text-xs text-[var(--app-text)] outline-none"
            />
            <input
              value={form.exclude_scope}
              onChange={(e) => setForm({ ...form, exclude_scope: e.target.value })}
              placeholder="排除范围（可选）"
              className="w-44 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-1.5 text-xs text-[var(--app-text)] outline-none"
            />
            <button type="button" onClick={save} disabled={saving} className="btn-primary ml-auto text-xs disabled:opacity-50">
              {saving ? <Loader2 size={13} className="inline animate-spin" /> : '保存'}
            </button>
          </div>
        </div>
      )}

      <div className="mt-6 space-y-2">
        {rules === null && (
          <div className="flex items-center justify-center py-24 text-[var(--app-muted)]">
            <Loader2 className="animate-spin" size={22} />
          </div>
        )}
        {rules !== null && rules.length === 0 && (
          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-12 text-center text-xs text-[var(--app-muted)]">
            还没有替换规则。规则按顺序作用于所有书籍正文，可用来去除广告、修正错字。
          </div>
        )}
        {rules?.map((r) => (
          <div key={r.id} className="flex items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] px-4 py-3">
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-bold text-[var(--app-text)]">
                {r.name || '(未命名规则)'}
                {r.is_regex && <span className="ml-2 rounded-md bg-brand-500/10 px-1.5 py-0.5 text-2xs font-bold text-brand-600">正则</span>}
                {r.group && <span className="ml-2 text-2xs font-normal text-[var(--app-muted)]">{r.group}</span>}
              </p>
              <p className="mt-0.5 truncate font-mono text-2xs text-[var(--app-muted)]">
                {r.pattern} → {r.replacement || '（删除）'}
              </p>
            </div>
            <button
              type="button"
              title="删除"
              onClick={() => remove(r)}
              className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-red-500"
            >
              <Trash2 size={16} />
            </button>
            <button
              type="button"
              role="switch"
              aria-checked={r.is_enabled}
              onClick={() => toggle(r)}
              className={`relative h-5 w-9 shrink-0 rounded-full transition ${r.is_enabled ? 'bg-brand-500' : 'bg-[var(--app-hover)]'}`}
            >
              <span
                className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all ${
                  r.is_enabled ? 'left-[18px]' : 'left-0.5'
                }`}
              />
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
