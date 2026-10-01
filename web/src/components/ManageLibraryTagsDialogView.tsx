import { useCallback, useEffect, useMemo, useState } from 'react'
import { Check, GripVertical, Loader2, Pencil, Plus, Tag, Trash2, X } from 'lucide-react'
import toast from 'react-hot-toast'

import { confirmAction } from './confirmAction'
import type { Library } from '../types'
import { useDragReorder } from '../hooks/useDragReorder'
import {
  MAX_LIBRARY_TAGS,
  filterLibrariesForTagging,
  normalizeTagName,
  tagNameError,
  type LibraryTag,
} from '../utils/libraryTags'

export type ManageLibraryTagsDialogProps = {
  tags: LibraryTag[]
  libraries: Library[]
  saving: boolean
  onCreate: (name: string) => Promise<string>
  onRename: (from: string, to: string) => Promise<void>
  onRemove: (name: string) => Promise<void>
  /** 按给定标签名顺序重排标签（拖拽排序的落点）。 */
  onReorder: (orderedNames: string[]) => Promise<void>
  /** 单个媒体库归类；tagName 为空表示移出所有标签。 */
  onAssign: (libraryId: string, tagName: string) => Promise<void>
  /** 批量归类；tagName 为空表示批量移出所有标签。一次写入。 */
  onAssignBatch: (libraryIds: string[], tagName: string) => Promise<void>
  onClose: () => void
}

/** 「未分类」在筛选/批量目标里的哨兵值，与标签名不会冲突。 */
const UNTAGGED = '__untagged__'

/**
 * 标签管理对话框：左侧维护标签（新建 / 重命名 / 删除），右侧把媒体库归入
 * 某个标签。一个媒体库同时只属于一个标签，未选择即为「未分类」。
 *
 * 右侧支持搜索、按当前标签筛选、多选后一次性批量归类，避免逐条点击下拉框。
 */
export function ManageLibraryTagsDialogView({
  tags,
  libraries,
  saving,
  onCreate,
  onRename,
  onRemove,
  onReorder,
  onAssign,
  onAssignBatch,
  onClose,
}: ManageLibraryTagsDialogProps) {
  const [draftName, setDraftName] = useState('')
  const [renaming, setRenaming] = useState('')
  const [renameDraft, setRenameDraft] = useState('')
  const [busyLibrary, setBusyLibrary] = useState('')
  const [keyword, setKeyword] = useState('')
  const [tagFilter, setTagFilter] = useState('')
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const [batchTag, setBatchTag] = useState('')
  const [applying, setApplying] = useState(false)
  const { draggingId, dragOverId, dragProps } = useDragReorder(
    tags.map((tag) => tag.name),
    onReorder,
  )

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  const tagByLibrary = useMemo(() => {
    const map = new Map<string, string>()
    for (const tag of tags) {
      for (const id of tag.library_ids) {
        if (!map.has(id)) map.set(id, tag.name)
      }
    }
    return map
  }, [tags])

  // 标签被删除/重命名后，批量目标与筛选值可能已经失效，落回默认项。
  useEffect(() => {
    if (batchTag && batchTag !== UNTAGGED && !tags.some((tag) => tag.name === batchTag)) {
      setBatchTag('')
    }
    if (tagFilter && tagFilter !== UNTAGGED && !tags.some((tag) => tag.name === tagFilter)) {
      setTagFilter('')
    }
  }, [tags, batchTag, tagFilter])

  // 媒体库被删除后清掉已选项，避免批量应用时带着不存在的 ID。
  useEffect(() => {
    const available = new Set(libraries.map((lib) => lib.id))
    setSelectedIds((prev) => {
      const next = prev.filter((id) => available.has(id))
      return next.length === prev.length ? prev : next
    })
  }, [libraries])

  const visibleLibraries = useMemo(() => {
    const searched = filterLibrariesForTagging(libraries, keyword)
    if (!tagFilter) return searched
    return searched.filter((lib) => {
      const current = tagByLibrary.get(lib.id) ?? ''
      return tagFilter === UNTAGGED ? current === '' : current === tagFilter
    })
  }, [libraries, keyword, tagFilter, tagByLibrary])

  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds])
  const visibleSelectedCount = visibleLibraries.reduce(
    (sum, lib) => sum + (selectedSet.has(lib.id) ? 1 : 0),
    0,
  )
  const allVisibleSelected = visibleLibraries.length > 0 && visibleSelectedCount === visibleLibraries.length

  const toggleLibrary = useCallback((libraryId: string) => {
    setSelectedIds((prev) =>
      prev.includes(libraryId) ? prev.filter((id) => id !== libraryId) : [...prev, libraryId],
    )
  }, [])

  const toggleVisible = useCallback(() => {
    const visibleIds = visibleLibraries.map((lib) => lib.id)
    setSelectedIds((prev) => {
      const current = new Set(prev)
      const everySelected = visibleIds.length > 0 && visibleIds.every((id) => current.has(id))
      if (everySelected) return prev.filter((id) => !visibleIds.includes(id))
      for (const id of visibleIds) current.add(id)
      return Array.from(current)
    })
  }, [visibleLibraries])

  const handleCreate = async () => {
    const error = tagNameError(draftName, tags)
    if (error) {
      toast.error(error)
      return
    }
    if (tags.length >= MAX_LIBRARY_TAGS) {
      toast.error(`最多 ${MAX_LIBRARY_TAGS} 个标签`)
      return
    }
    const created = await onCreate(normalizeTagName(draftName))
    if (created) {
      setDraftName('')
      toast.success(`标签「${created}」已创建`)
    } else {
      toast.error('标签创建失败')
    }
  }

  const handleRename = async (from: string) => {
    const error = tagNameError(renameDraft, tags, from)
    if (error) {
      toast.error(error)
      return
    }
    await onRename(from, renameDraft)
    setRenaming('')
    setRenameDraft('')
  }

  const handleRemove = async (tag: LibraryTag) => {
    const confirmed = await confirmAction({
      title: '删除标签',
      message: `确定删除标签「${tag.name}」？\n媒体库本身不会被删除，只会回到「未分类」。`,
      confirmText: '删除',
    })
    if (!confirmed) return
    await onRemove(tag.name)
  }

  const handleAssign = async (library: Library, tagName: string) => {
    setBusyLibrary(library.id)
    try {
      await onAssign(library.id, tagName)
    } finally {
      setBusyLibrary('')
    }
  }

  const handleApplyBatch = async () => {
    if (selectedIds.length === 0 || applying) return
    setApplying(true)
    try {
      await onAssignBatch(selectedIds, batchTag)
      const count = selectedIds.length
      toast.success(
        batchTag ? `已将 ${count} 个媒体库归入「${batchTag}」` : `已将 ${count} 个媒体库移出标签`,
      )
      setSelectedIds([])
    } finally {
      setApplying(false)
    }
  }

  const busy = saving || applying

  return (
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black/35 p-2 backdrop-blur-sm sm:p-4"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="flex h-[calc(100dvh-1rem)] w-full max-w-5xl flex-col overflow-hidden rounded-2xl border border-white/70 bg-[var(--app-panel)] shadow-2xl sm:h-[82vh] sm:rounded-3xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-4 py-3 sm:px-6 sm:py-4">
          <div className="flex items-center gap-2">
            <Tag size={18} className="text-brand-500" />
            <h3 className="font-display text-base font-bold text-[var(--app-text)] sm:text-lg">管理标签</h3>
            {busy && <Loader2 size={14} className="animate-spin text-[var(--app-muted)]" />}
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl p-1.5 text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
            title="关闭"
          >
            <X size={20} />
          </button>
        </div>

        <div className="grid flex-1 grid-cols-1 gap-4 overflow-hidden p-3 sm:p-6 md:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)]">
          <section className="flex min-h-0 flex-col gap-3">
            <h4 className="text-xs font-bold uppercase tracking-widest text-[var(--app-muted)]">标签</h4>
            <div className="flex items-center gap-1.5">
              <input
                value={draftName}
                onChange={(event) => setDraftName(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    event.preventDefault()
                    void handleCreate()
                  }
                }}
                maxLength={24}
                placeholder="新建标签名"
                className="input-base !py-2 text-sm"
              />
              <button
                type="button"
                onClick={() => void handleCreate()}
                disabled={busy || !draftName.trim()}
                className="btn-outline shrink-0 !px-3 !py-2 text-xs"
                title="创建标签"
              >
                <Plus size={14} />
                新建
              </button>
            </div>

            <div className="min-h-0 flex-1 space-y-1.5 overflow-y-auto pr-1">
              {tags.length === 0 ? (
                <p className="rounded-xl border border-dashed border-[var(--app-border)] px-3 py-6 text-center text-xs text-[var(--app-muted)]">
                  还没有标签。先创建一个，再把媒体库归入其中。
                </p>
              ) : (
                tags.map((tag) => {
                  const drag = dragProps(tag.name)
                  const isDragOver = dragOverId === tag.name && draggingId !== tag.name
                  return (
                    <div
                      key={tag.name}
                      onDragOver={drag.onDragOver}
                      onDragLeave={drag.onDragLeave}
                      onDrop={drag.onDrop}
                      className={`flex items-center gap-2 rounded-xl border px-2.5 py-2 transition-colors ${
                        isDragOver
                          ? 'border-brand-500/60 bg-brand-500/5'
                          : 'border-[var(--app-border)] bg-[var(--app-panel-soft)]'
                      }`}
                    >
                      {renaming === tag.name ? (
                        <>
                          <input
                            autoFocus
                            value={renameDraft}
                            onChange={(event) => setRenameDraft(event.target.value)}
                            onKeyDown={(event) => {
                              if (event.key === 'Enter') {
                                event.preventDefault()
                                void handleRename(tag.name)
                              }
                              if (event.key === 'Escape') {
                                setRenaming('')
                                setRenameDraft('')
                              }
                            }}
                            maxLength={24}
                            className="min-w-0 flex-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-panel)] px-2 py-1 text-xs font-semibold text-[var(--app-text)] outline-none focus:border-brand-500"
                          />
                          <button
                            type="button"
                            onClick={() => void handleRename(tag.name)}
                            className="rounded-lg p-1 text-brand-500 transition-colors hover:bg-[var(--app-hover)]"
                            title="保存名称"
                          >
                            <Check size={14} />
                          </button>
                          <button
                            type="button"
                            onClick={() => {
                              setRenaming('')
                              setRenameDraft('')
                            }}
                            className="rounded-lg p-1 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)]"
                            title="取消"
                          >
                            <X size={14} />
                          </button>
                        </>
                      ) : (
                        <>
                          <button
                            type="button"
                            draggable={drag.draggable}
                            onDragStart={drag.onDragStart}
                            onDragEnd={drag.onDragEnd}
                            className={`-ml-1 shrink-0 cursor-grab rounded-lg p-1 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)] ${
                              draggingId === tag.name ? 'opacity-40' : ''
                            }`}
                            title="拖拽调整标签顺序"
                            aria-label="拖拽调整标签顺序"
                          >
                            <GripVertical size={13} />
                          </button>
                          <button
                            type="button"
                            onClick={() => setTagFilter(tag.name)}
                            className="min-w-0 flex-1 truncate text-left text-sm font-bold text-[var(--app-text)] transition-colors hover:text-brand-500"
                            title={`只看「${tag.name}」标签下的媒体库`}
                          >
                            {tag.name}
                          </button>
                          <span className="shrink-0 font-mono text-[10px] text-[var(--app-muted)]">
                            {tag.library_ids.length}
                          </span>
                          <button
                            type="button"
                            onClick={() => {
                              setRenaming(tag.name)
                              setRenameDraft(tag.name)
                            }}
                            className="rounded-lg p-1 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)] hover:text-brand-500"
                            title="重命名"
                          >
                            <Pencil size={13} />
                          </button>
                          <button
                            type="button"
                            onClick={() => void handleRemove(tag)}
                            className="rounded-lg p-1 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)] hover:text-red-500"
                            title="删除标签"
                          >
                            <Trash2 size={13} />
                          </button>
                        </>
                      )}
                    </div>
                  )
                })
              )}
            </div>
          </section>

          <section className="flex min-h-0 flex-col gap-3 border-t border-[var(--app-border)] pt-4 md:border-l md:border-t-0 md:pl-4 md:pt-0">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h4 className="text-xs font-bold uppercase tracking-widest text-[var(--app-muted)]">
                媒体库归类（{libraries.length}）
              </h4>
              <span className="text-xs text-[var(--app-muted)]">勾选多个后可一次性设置标签</span>
            </div>

            <div className="flex flex-wrap items-center gap-1.5">
              <input
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                placeholder="搜索名称 / 路径 / 类型"
                className="input-base h-9 min-w-[10rem] flex-1 !py-0 text-xs"
              />
              <select
                value={tagFilter}
                onChange={(event) => setTagFilter(event.target.value)}
                className="input-base h-9 w-32 shrink-0 !py-0 text-xs"
                title="筛选媒体库"
              >
                <option value="">全部标签</option>
                <option value={UNTAGGED}>未分类</option>
                {tags.map((tag) => (
                  <option key={tag.name} value={tag.name}>
                    {tag.name}
                  </option>
                ))}
              </select>
            </div>

            <div className="flex flex-wrap items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-2.5 py-2">
              <button
                type="button"
                onClick={toggleVisible}
                disabled={visibleLibraries.length === 0}
                className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-panel)] px-2.5 py-1 text-xs font-bold text-[var(--app-subtle)] transition-colors hover:border-brand-500/40 hover:text-brand-500 disabled:opacity-40"
                title="全选 / 取消全选当前列表中的媒体库"
              >
                <Check size={13} />
                {allVisibleSelected ? '取消全选' : '全选'}
              </button>
              <span className="text-xs font-semibold text-[var(--app-subtle)]">
                已选 {selectedIds.length} 个
              </span>
              <select
                value={batchTag}
                onChange={(event) => setBatchTag(event.target.value)}
                className="input-base ml-auto h-8 w-36 shrink-0 !py-0 text-xs"
                title="选择要批量设置的标签"
              >
                <option value="">未分类（移出标签）</option>
                {tags.map((tag) => (
                  <option key={tag.name} value={tag.name}>
                    {tag.name}
                  </option>
                ))}
              </select>
              <button
                type="button"
                onClick={() => void handleApplyBatch()}
                disabled={busy || selectedIds.length === 0}
                className="btn-outline shrink-0 !px-3 !py-1.5 text-xs"
                title="把选中媒体库的标签一次性设为左侧所选值"
              >
                {applying && <Loader2 size={13} className="animate-spin" />}
                应用到已选
              </button>
              {selectedIds.length > 0 && (
                <button
                  type="button"
                  onClick={() => setSelectedIds([])}
                  disabled={applying}
                  className="rounded-lg p-1 text-[var(--app-muted)] transition-colors hover:bg-[var(--app-hover)]"
                  title="清空选择"
                >
                  <X size={13} />
                </button>
              )}
            </div>

            <div className="min-h-0 flex-1 space-y-1.5 overflow-y-auto pr-1">
              {visibleLibraries.length === 0 ? (
                <p className="rounded-xl border border-dashed border-[var(--app-border)] px-3 py-6 text-center text-xs text-[var(--app-muted)]">
                  {libraries.length === 0 ? '暂无可归类的媒体库。' : '没有符合当前筛选条件的媒体库。'}
                </p>
              ) : (
                visibleLibraries.map((library) => {
                  const current = tagByLibrary.get(library.id) ?? ''
                  const checked = selectedSet.has(library.id)
                  return (
                    <div
                      key={library.id}
                      className={`flex items-center gap-2 rounded-xl border px-2.5 py-2 transition-colors ${
                        checked
                          ? 'border-brand-500/50 bg-brand-500/5'
                          : 'border-[var(--app-border)] bg-[var(--app-panel-soft)]'
                      }`}
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggleLibrary(library.id)}
                        disabled={applying}
                        className="h-4 w-4 shrink-0 accent-brand-400"
                        title="勾选后可批量设置标签"
                      />
                      <span
                        className="min-w-0 flex-1 truncate text-xs font-semibold text-[var(--app-text)]"
                        title={library.path || library.name}
                      >
                        {library.name}
                      </span>
                      {busyLibrary === library.id && (
                        <Loader2 size={13} className="shrink-0 animate-spin text-[var(--app-muted)]" />
                      )}
                      <select
                        value={current}
                        disabled={busy || busyLibrary === library.id}
                        onChange={(event) => void handleAssign(library, event.target.value)}
                        className="input-base h-8 w-32 shrink-0 !py-0 text-xs"
                        title="选择该媒体库所属标签"
                      >
                        <option value="">未分类</option>
                        {tags.map((tag) => (
                          <option key={tag.name} value={tag.name}>
                            {tag.name}
                          </option>
                        ))}
                      </select>
                    </div>
                  )
                })
              )}
            </div>
          </section>
        </div>

        <div className="flex items-center justify-between gap-3 border-t border-[var(--app-border)] px-4 py-3 sm:px-6">
          <span className="text-xs text-[var(--app-muted)]">标签按用户保存；一个媒体库同时只属于一个标签。</span>
          <button type="button" onClick={onClose} className="btn-outline !px-4 !py-2 text-sm">
            完成
          </button>
        </div>
      </div>
    </div>
  )
}
