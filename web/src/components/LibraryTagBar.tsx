import { useEffect, useMemo, useRef, useState } from 'react'
import { Layers, Plus, Settings2, X } from 'lucide-react'

import { MAX_LIBRARY_TAGS } from '../utils/libraryTags'
import type { LibraryTagTab } from '../utils/libraryTags'

/**
 * 媒体库标签栏：固定「全部」+ 每个标签一个页签，选中后只展示该标签下的媒体库。
 * 只是管理员/普通用户都能用的展示层过滤，不改动媒体库本身。
 */
export function LibraryTagBar({
  tabs,
  selectedTagId,
  onSelect,
  onCreate,
  onManage,
  manageLabel = '管理标签',
  showCreate = true,
  busy = false,
}: {
  tabs: LibraryTagTab[]
  selectedTagId: string
  onSelect: (tagId: string) => void
  onCreate?: (name: string) => void
  onManage?: () => void
  manageLabel?: string
  showCreate?: boolean
  busy?: boolean
}) {
  const [creating, setCreating] = useState(false)
  const [draft, setDraft] = useState('')
  const inputRef = useRef<HTMLInputElement | null>(null)
  // 同一帧内 blur 与 Enter 都可能触发提交：用一个标记保证只提交一次。
  const submittedRef = useRef(false)

  useEffect(() => {
    if (creating) {
      submittedRef.current = false
      inputRef.current?.focus()
    }
  }, [creating])

  const hasTags = useMemo(() => tabs.some((tab) => !tab.isAll), [tabs])

  const submit = () => {
    if (submittedRef.current) return
    submittedRef.current = true
    const name = draft.trim()
    setDraft('')
    setCreating(false)
    if (!name) return
    onCreate?.(name)
  }

  const cancel = () => {
    submittedRef.current = true
    setDraft('')
    setCreating(false)
  }

  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-[var(--app-border)] pb-3">
      <div className="flex flex-wrap items-center gap-1.5">
        {tabs.map((tab) => {
          const active = tab.id === selectedTagId
          return (
            <button
              key={tab.id}
              type="button"
              onClick={() => onSelect(tab.id)}
              aria-pressed={active}
              className={`inline-flex items-center gap-1.5 rounded-xl border px-3 py-1.5 text-xs font-bold transition-colors ${
                active
                  ? 'border-brand-500/60 bg-brand-500/10 text-brand-600'
                  : 'border-[var(--app-border)] bg-[var(--app-panel-soft)] text-[var(--app-subtle)] hover:border-brand-500/40 hover:text-[var(--app-text)]'
              }`}
              title={tab.isAll ? '显示全部媒体库' : `只看「${tab.name}」标签下的媒体库`}
            >
              {tab.isAll && <Layers size={13} />}
              <span className="max-w-[8rem] truncate">{tab.name}</span>
              <span className={`font-mono text-[10px] ${active ? 'text-brand-500' : 'text-[var(--app-muted)]'}`}>
                {tab.count}
              </span>
            </button>
          )
        })}
      </div>

      {(showCreate || onManage) && (
        <div className="flex flex-wrap items-center gap-1.5">
          {showCreate && onCreate && (creating || !hasTags) && (
            <div className="inline-flex items-center gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-2 py-1">
              <input
                ref={inputRef}
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    event.preventDefault()
                    submit()
                  }
                  if (event.key === 'Escape') {
                    event.preventDefault()
                    setDraft('')
                    setCreating(false)
                  }
                }}
                onBlur={() => submit()}
                maxLength={24}
                placeholder="新标签名"
                className="h-6 w-24 bg-transparent text-xs font-semibold text-[var(--app-text)] outline-none placeholder:text-[var(--app-muted)]"
                title={`最多 24 个字，当前最多 ${MAX_LIBRARY_TAGS} 个标签`}
              />
              <button
                type="button"
                onMouseDown={(event) => event.preventDefault()}
                onClick={submit}
                disabled={busy || !draft.trim()}
                className="rounded-lg p-1 text-brand-500 transition-colors hover:bg-[var(--app-hover)] disabled:opacity-40"
                title="创建标签"
              >
                <Plus size={13} />
              </button>
              <button
                type="button"
                onMouseDown={(event) => event.preventDefault()}
                onClick={cancel}
                className="rounded-lg p-1 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)]"
                title="取消"
              >
                <X size={13} />
              </button>
            </div>
          )}

          {showCreate && onCreate && !creating && hasTags && (
            <button
              type="button"
              onClick={() => setCreating(true)}
              disabled={busy || tabs.length - 1 >= MAX_LIBRARY_TAGS}
              className="inline-flex items-center gap-1 rounded-xl border border-dashed border-[var(--app-border)] px-2.5 py-1.5 text-xs font-bold text-[var(--app-muted)] transition-colors hover:border-brand-500/50 hover:text-brand-500 disabled:opacity-40"
              title="新建标签"
            >
              <Plus size={13} />
              <span>新建标签</span>
            </button>
          )}

          {onManage && (
            <button
              type="button"
              onClick={onManage}
              className="inline-flex items-center gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-2.5 py-1.5 text-xs font-bold text-[var(--app-subtle)] transition-colors hover:border-brand-500/40 hover:text-brand-500"
              title="重命名 / 删除标签，调整标签下的媒体库"
            >
              <Settings2 size={13} />
              <span>{manageLabel}</span>
            </button>
          )}
        </div>
      )}
    </div>
  )
}
