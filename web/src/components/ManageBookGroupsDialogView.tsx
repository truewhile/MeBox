import { useEffect, useState } from 'react'
import { Check, GripVertical, Loader2, Pencil, Plus, Trash2, X } from 'lucide-react'
import toast from 'react-hot-toast'

import { confirmAction } from './confirmAction'
import { useDragReorder } from '../hooks/useDragReorder'
import { MAX_BOOK_GROUPS, bookGroupNameError, normalizeBookGroupName } from '../utils/readerBookGroups'
import type { BookGroup } from '../api/reader'

export type ManageBookGroupsDialogProps = {
  groups: BookGroup[]
  /** 分组名 → 当前书架里实际属于该组的书籍数（不统计已失效的 ID）。 */
  bookCounts: Record<string, number>
  saving: boolean
  onCreate: (name: string) => Promise<string>
  onRename: (from: string, to: string) => Promise<void>
  onRemove: (name: string) => Promise<void>
  /** 按给定分组名顺序重排（拖拽排序的落点）。 */
  onReorder: (orderedNames: string[]) => Promise<void>
  onClose: () => void
}

/**
 * 分组管理对话框（对齐影视模块的管理标签弹窗，去掉成员分配那半边）：
 * 新建 / 重命名 / 删除分组，拖拽调整分组栏顺序。
 *
 * 书籍归组不在这里做，而是在书架卡片上的「分组」按钮里逐个/批量指定 —— 书可能有上千本，
 * 逐个列在弹窗里既慢又难找。
 */
export function ManageBookGroupsDialogView({
  groups,
  bookCounts,
  saving,
  onCreate,
  onRename,
  onRemove,
  onReorder,
  onClose,
}: ManageBookGroupsDialogProps) {
  const [draftName, setDraftName] = useState('')
  const [renaming, setRenaming] = useState('')
  const [renameDraft, setRenameDraft] = useState('')
  const { draggingId, dragOverId, dragProps } = useDragReorder(
    groups.map((group) => group.name),
    onReorder,
  )

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  const submitCreate = () => {
    const name = normalizeBookGroupName(draftName)
    if (!name) return
    const err = bookGroupNameError(draftName, groups)
    if (err) {
      toast.error(err)
      return
    }
    setDraftName('')
    void onCreate(name)
  }

  const submitRename = (from: string) => {
    const name = normalizeBookGroupName(renameDraft)
    setRenaming('')
    setRenameDraft('')
    if (!name || name === from) return
    const err = bookGroupNameError(renameDraft, groups, from)
    if (err) {
      toast.error(err)
      return
    }
    void onRename(from, name)
  }

  const handleRemove = async (name: string) => {
    const ok = await confirmAction({
      title: '删除分组',
      message: `删除「${name}」后，组内的书会回到「未分组」，书籍本身不会被移除。确定吗？`,
      confirmText: '删除',
      danger: true,
    })
    if (!ok) return
    void onRemove(name)
  }

  return (
    <div
      className="fixed inset-0 z-[110] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="管理分组"
        className="flex max-h-[85vh] w-full max-w-md flex-col overflow-hidden rounded-3xl border border-[var(--app-border)] bg-[var(--app-panel)] shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-5 py-4">
          <div>
            <h3 className="text-sm font-bold text-[var(--app-text)]">管理分组</h3>
            <p className="mt-0.5 text-2xs text-[var(--app-muted)]">
              拖拽调整顺序 · 最多 {MAX_BOOK_GROUPS} 个分组
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

        <div className="flex items-center gap-2 border-b border-[var(--app-border)] px-5 py-3">
          <input
            value={draftName}
            onChange={(event) => setDraftName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                submitCreate()
              }
            }}
            maxLength={24}
            placeholder="新分组名"
            className="h-9 min-w-0 flex-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 text-xs font-semibold text-[var(--app-text)] outline-none placeholder:text-[var(--app-muted)] focus:border-brand-500"
          />
          <button
            type="button"
            onClick={submitCreate}
            disabled={saving || !draftName.trim() || groups.length >= MAX_BOOK_GROUPS}
            className="inline-flex shrink-0 items-center gap-1 rounded-xl border border-[var(--app-border)] px-3 py-2 text-xs font-bold text-brand-600 transition hover:bg-[var(--app-hover)] disabled:opacity-40"
          >
            <Plus size={13} /> 新建
          </button>
        </div>

        <div className="flex-1 overflow-y-auto p-2">
          {groups.length === 0 ? (
            <p className="py-10 text-center text-xs text-[var(--app-muted)]">
              还没有分组，先在上方新建一个，再到书籍卡片上用「分组」按钮把书放进去
            </p>
          ) : (
            <div className="space-y-1">
              {groups.map((group) => {
                const isRenaming = renaming === group.name
                const dragging = draggingId === group.name
                const over = dragOverId === group.name
                return (
                  <div
                    key={group.name}
                    {...dragProps(group.name)}
                    className={`flex items-center gap-2 rounded-xl border px-2 py-2 transition ${
                      dragging
                        ? 'border-brand-500/60 opacity-60'
                        : over
                          ? 'border-brand-500/60 bg-brand-500/5'
                          : 'border-transparent hover:bg-[var(--app-hover)]'
                    }`}
                  >
                    <span
                      className="shrink-0 cursor-grab p-1 text-[var(--app-muted)] active:cursor-grabbing"
                      title="拖拽调整顺序"
                    >
                      <GripVertical size={14} />
                    </span>

                    {isRenaming ? (
                      <>
                        <input
                          autoFocus
                          value={renameDraft}
                          onChange={(event) => setRenameDraft(event.target.value)}
                          onKeyDown={(event) => {
                            if (event.key === 'Enter') {
                              event.preventDefault()
                              submitRename(group.name)
                            }
                            if (event.key === 'Escape') {
                              setRenaming('')
                              setRenameDraft('')
                            }
                          }}
                          maxLength={24}
                          className="h-7 min-w-0 flex-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-2 text-xs font-semibold text-[var(--app-text)] outline-none focus:border-brand-500"
                        />
                        <button
                          type="button"
                          onClick={() => submitRename(group.name)}
                          className="rounded-lg p-1.5 text-brand-600 hover:bg-[var(--app-hover)]"
                          title="保存"
                        >
                          <Check size={14} />
                        </button>
                        <button
                          type="button"
                          onClick={() => {
                            setRenaming('')
                            setRenameDraft('')
                          }}
                          className="rounded-lg p-1.5 text-[var(--app-muted)] hover:bg-[var(--app-hover)]"
                          title="取消"
                        >
                          <X size={14} />
                        </button>
                      </>
                    ) : (
                      <>
                        <span className="min-w-0 flex-1 truncate text-xs font-bold text-[var(--app-text)]">
                          {group.name}
                        </span>
                        <span className="shrink-0 font-mono text-[10px] text-[var(--app-muted)]">
                          {bookCounts[group.name] ?? 0} 本
                        </span>
                        <button
                          type="button"
                          onClick={() => {
                            setRenaming(group.name)
                            setRenameDraft(group.name)
                          }}
                          className="rounded-lg p-1.5 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
                          title="重命名"
                        >
                          <Pencil size={13} />
                        </button>
                        <button
                          type="button"
                          onClick={() => void handleRemove(group.name)}
                          className="rounded-lg p-1.5 text-[var(--app-muted)] hover:bg-red-500/10 hover:text-red-500"
                          title="删除分组"
                        >
                          <Trash2 size={13} />
                        </button>
                      </>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {saving && (
          <div className="flex items-center justify-center gap-2 border-t border-[var(--app-border)] py-2 text-2xs text-[var(--app-muted)]">
            <Loader2 size={12} className="animate-spin" /> 正在保存…
          </div>
        )}
      </div>
    </div>
  )
}
