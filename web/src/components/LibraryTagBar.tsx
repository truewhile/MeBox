import { useEffect, useMemo, useRef, useState, type DragEvent } from 'react'
import { GripVertical, Layers, Plus, Settings2, X } from 'lucide-react'

import { ALL_TAG_ID, MAX_LIBRARY_TAGS } from '../utils/libraryTags'
import type { LibraryTagTab } from '../utils/libraryTags'

/**
 * 媒体库标签栏：固定「全部」+ 每个标签一个页签，选中后只展示该标签下的媒体库。
 * 只是管理员/普通用户都能用的展示层过滤，不改动媒体库本身。
 * 传入 onReorder 时标签页签可拖拽排序：拖拽只作用于「全部」之后的标签，
 * 「全部」永远固定在第一位，不参与拖动。
 */
export function LibraryTagBar({
  tabs,
  selectedTagId,
  onSelect,
  onCreate,
  onManage,
  onReorder,
  manageLabel = '管理标签',
  showCreate = true,
  busy = false,
}: {
  tabs: LibraryTagTab[]
  selectedTagId: string
  onSelect: (tagId: string) => void
  onCreate?: (name: string) => void
  onManage?: () => void
  /** 拖拽排序的落点：收到的是拖拽后的标签名顺序（不含「全部」）。 */
  onReorder?: (orderedNames: string[]) => void | Promise<void>
  manageLabel?: string
  showCreate?: boolean
  busy?: boolean
}) {
  const [creating, setCreating] = useState(false)
  const [draft, setDraft] = useState('')
  const inputRef = useRef<HTMLInputElement | null>(null)
  // 同一帧内 blur 与 Enter 都可能触发提交：用一个标记保证只提交一次。
  const submittedRef = useRef(false)
  const [draggingId, setDraggingId] = useState<string | null>(null)
  const [dragOverId, setDragOverId] = useState<string | null>(null)

  useEffect(() => {
    if (creating) {
      submittedRef.current = false
      inputRef.current?.focus()
    }
  }, [creating])

  const hasTags = useMemo(() => tabs.some((tab) => !tab.isAll), [tabs])

  /** 拖拽结束后计算落点顺序并回调。 */
  const handleDrop = (e: DragEvent, overTabId: string) => {
    e.preventDefault()
    const fromId = draggingId ?? e.dataTransfer.getData('text/plain')
    setDraggingId(null)
    setDragOverId(null)
    if (!onReorder || !fromId || fromId === ALL_TAG_ID || fromId === overTabId) return
    const names = tabs
      .filter((tab) => !tab.isAll)
      .map((tab) => tab.id)
    const fromIndex = names.indexOf(fromId)
    const overIndex = names.indexOf(overTabId)
    if (fromIndex < 0 || overIndex < 0) return
    const next = [...names]
    const [moved] = next.splice(fromIndex, 1)
    next.splice(overIndex, 0, moved)
    void onReorder(next)
  }

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
          const draggable = !tab.isAll && Boolean(onReorder)
          return (
            <button
              key={tab.id}
              type="button"
              onClick={() => onSelect(tab.id)}
              aria-pressed={active}
              draggable={draggable}
              onDragStart={(e) => {
                if (!draggable) return
                e.dataTransfer.effectAllowed = 'move'
                e.dataTransfer.setData('text/plain', tab.id)
                setDraggingId(tab.id)
                setDragOverId(null)
              }}
              onDragOver={(e) => {
                if (draggingId === tab.id || tab.isAll) return
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
                if (dragOverId !== tab.id) setDragOverId(tab.id)
              }}
              onDragLeave={() => {
                setDragOverId((prev) => (prev === tab.id ? null : prev))
              }}
              onDrop={(e) => handleDrop(e, tab.id)}
              onDragEnd={() => {
                setDraggingId(null)
                setDragOverId(null)
              }}
              className={`group inline-flex cursor-default items-center gap-1.5 rounded-xl border px-3 py-1.5 text-xs font-bold transition-colors ${
                draggingId === tab.id
                  ? 'opacity-40'
                  : dragOverId === tab.id
                    ? 'border-brand-500 bg-brand-500/10 text-brand-600'
                    : active
                      ? 'border-brand-500/60 bg-brand-500/10 text-brand-600'
                      : 'border-[var(--app-border)] bg-[var(--app-panel-soft)] text-[var(--app-subtle)] hover:border-brand-500/40 hover:text-[var(--app-text)]'
              }`}
              title={
                tab.isAll
                  ? '显示全部媒体库'
                  : draggable
                    ? `只看「${tab.name}」标签下的媒体库（拖拽可调整顺序）`
                    : `只看「${tab.name}」标签下的媒体库`
              }
            >
              {tab.isAll && <Layers size={13} />}
              <span className="max-w-[8rem] truncate">{tab.name}</span>
              <span className={`font-mono text-[10px] ${active ? 'text-brand-500' : 'text-[var(--app-muted)]'}`}>
                {tab.count}
              </span>
              {draggable && (
                <GripVertical
                  size={12}
                  className="shrink-0 cursor-grab text-[var(--app-muted)] opacity-0 transition-opacity group-hover:opacity-100 active:cursor-grabbing"
                  aria-hidden
                />
              )}
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
