import { useMemo, useState } from 'react'
import { Check, Minus, Search, X } from 'lucide-react'

import type { ReaderSource } from '../../api/reader'

/**
 * SearchScopeDialog 搜索范围弹窗（对应 legado 搜索页的 SearchScopeDialog）。
 *
 * 只列出书源维度：默认全选所有「已启用」书源（对应 legado SearchScope("") 的
 * 「全部书源」语义），取消勾选即可把本次搜索收敛到指定书源。已停用的书源会显示
 * 但不可勾选，让用户知道它们不会被搜索。分组标题支持整组勾选/取消（kind 批量选择）。
 *
 * 确认时若「全部启用书源」都被选中，回传空数组表示「全部」——与后端
 * enabledSourcesForScope 的空范围语义一致，也让后续新增的书源自动纳入搜索。
 */
export function SearchScopeDialog({
  sources,
  selectedIds,
  onConfirm,
  onClose,
}: {
  sources: ReaderSource[]
  /** 已保存的搜索范围；空数组 = 全部启用书源（默认全选）。 */
  selectedIds: string[]
  onConfirm: (ids: string[]) => void
  onClose: () => void
}) {
  const enabled = useMemo(() => sources.filter((s) => s.enabled), [sources])
  const enabledIds = useMemo(() => enabled.map((s) => s.id), [enabled])

  // 初始选中：保存过范围就用它（过滤掉已删除/停用的），否则默认全选已启用。
  const [selected, setSelected] = useState<Set<string>>(() => {
    const valid = selectedIds.filter((id) => enabledIds.includes(id))
    return new Set(valid.length > 0 ? valid : enabledIds)
  })
  const [filter, setFilter] = useState('')

  const keyword = filter.trim().toLowerCase()
  const visible = useMemo(
    () =>
      sources.filter(
        (s) => !keyword || s.name.toLowerCase().includes(keyword) || (s.group ?? '').toLowerCase().includes(keyword),
      ),
    [sources, keyword],
  )
  // 按分组聚合并保持书源原顺序（ListSources 已按 customOrder 排好）。
  const groups = useMemo(() => {
    const map = new Map<string, ReaderSource[]>()
    for (const s of visible) {
      const key = (s.group ?? '').trim() || '未分组'
      const list = map.get(key)
      if (list) list.push(s)
      else map.set(key, [s])
    }
    return [...map.entries()].map(([name, items]) => ({ name, items }))
  }, [visible])

  const pickedCount = enabledIds.filter((id) => selected.has(id)).length
  const allSelected = pickedCount === enabledIds.length && enabledIds.length > 0

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const toggleGroup = (ids: string[]) =>
    setSelected((prev) => {
      const next = new Set(prev)
      const allIn = ids.every((id) => next.has(id))
      ids.forEach((id) => (allIn ? next.delete(id) : next.add(id)))
      return next
    })

  // 全选/清空只作用于当前可见（可能被搜索词过滤）的已启用书源。
  const visibleEnabledIds = visible.filter((s) => s.enabled).map((s) => s.id)
  const selectVisible = () => setSelected((prev) => new Set([...prev, ...visibleEnabledIds]))
  const clearVisible = () =>
    setSelected((prev) => {
      const next = new Set(prev)
      visibleEnabledIds.forEach((id) => next.delete(id))
      return next
    })

  const confirm = () => {
    const picked = enabledIds.filter((id) => selected.has(id))
    // 全选（含边界：启用源为空）→ 空数组 = 全部，新增书源自动纳入。
    onConfirm(picked.length === enabledIds.length ? [] : picked)
  }

  return (
    <div
      className="fixed inset-0 z-[110] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="flex max-h-[80vh] w-full max-w-lg flex-col overflow-hidden rounded-3xl border border-[var(--app-border)] bg-[var(--app-panel)] shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-5 py-4">
          <div className="min-w-0">
            <h3 className="text-sm font-bold text-[var(--app-text)]">搜索范围</h3>
            <p className="mt-0.5 text-2xs text-[var(--app-muted)]">
              默认搜索全部启用书源 · 已启用 {enabledIds.length} 个
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl p-1.5 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
            title="关闭"
          >
            <X size={18} />
          </button>
        </div>

        <div className="flex items-center gap-2 border-b border-[var(--app-border)] px-4 py-3">
          <div className="flex flex-1 items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-1.5">
            <Search size={13} className="shrink-0 text-[var(--app-muted)]" />
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="过滤书源名 / 分组"
              className="w-full bg-transparent text-xs text-[var(--app-text)] outline-none placeholder:text-[var(--app-muted)]"
            />
          </div>
          <button type="button" onClick={selectVisible} className="btn-outline text-2xs">
            全选
          </button>
          <button type="button" onClick={clearVisible} className="btn-outline text-2xs">
            清空
          </button>
        </div>

        <div className="min-h-[160px] flex-1 overflow-y-auto p-2">
          {sources.length === 0 ? (
            <p className="py-12 text-center text-xs text-[var(--app-muted)]">还没有书源，先去书源管理导入</p>
          ) : groups.length === 0 ? (
            <p className="py-12 text-center text-xs text-[var(--app-muted)]">没有匹配「{filter.trim()}」的书源</p>
          ) : (
            <div className="space-y-2">
              {groups.map((group) => {
                const groupEnabledIds = group.items.filter((s) => s.enabled).map((s) => s.id)
                const groupChecked = groupEnabledIds.length > 0 && groupEnabledIds.every((id) => selected.has(id))
                const groupPartial = !groupChecked && groupEnabledIds.some((id) => selected.has(id))
                return (
                  <div key={group.name}>
                    <button
                      type="button"
                      disabled={groupEnabledIds.length === 0}
                      onClick={() => toggleGroup(groupEnabledIds)}
                      className="flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left hover:bg-[var(--app-hover)] disabled:cursor-default"
                    >
                      <CheckMark checked={groupChecked} partial={groupPartial} disabled={groupEnabledIds.length === 0} />
                      <span className="min-w-0 flex-1 truncate text-2xs font-bold uppercase tracking-wide text-[var(--app-muted)]">
                        {group.name}
                      </span>
                      <span className="shrink-0 text-2xs text-[var(--app-subtle)]">{group.items.length}</span>
                    </button>
                    <div className="mt-0.5 space-y-0.5">
                      {group.items.map((src) => (
                        <button
                          key={src.id}
                          type="button"
                          disabled={!src.enabled}
                          onClick={() => toggle(src.id)}
                          className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left transition hover:bg-[var(--app-hover)] disabled:cursor-default disabled:opacity-50"
                        >
                          <CheckMark checked={src.enabled && selected.has(src.id)} disabled={!src.enabled} />
                          <span className="min-w-0 flex-1 truncate text-xs text-[var(--app-text)]">{src.name}</span>
                          {!src.enabled && (
                            <span className="shrink-0 rounded bg-[var(--app-hover)] px-1.5 py-0.5 text-2xs font-bold text-[var(--app-muted)]">
                              已禁用
                            </span>
                          )}
                        </button>
                      ))}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </div>

        <div className="flex items-center gap-3 border-t border-[var(--app-border)] px-5 py-3">
          <span className="flex-1 text-2xs text-[var(--app-muted)]">
            {allSelected ? '全部书源' : `已选 ${pickedCount} / ${enabledIds.length} 个书源`}
          </span>
          <button type="button" onClick={onClose} className="btn-outline text-xs">
            取消
          </button>
          <button type="button" onClick={confirm} disabled={pickedCount === 0} className="btn-primary text-xs disabled:opacity-50">
            确定
          </button>
        </div>
      </div>
    </div>
  )
}

/** 勾选标记（纯展示，点击由外层按钮承载，避免交互元素嵌套）。 */
function CheckMark({ checked, partial = false, disabled = false }: { checked: boolean; partial?: boolean; disabled?: boolean }) {
  return (
    <span
      aria-hidden
      className={`flex h-4 w-4 shrink-0 items-center justify-center rounded border transition ${
        checked || partial ? 'border-brand-500 bg-brand-500 text-white' : 'border-[var(--app-border)] bg-[var(--app-panel-soft)]'
      } ${disabled ? 'opacity-40' : ''}`}
    >
      {checked && <Check size={11} strokeWidth={3} />}
      {!checked && partial && <Minus size={11} strokeWidth={3} />}
    </span>
  )
}
