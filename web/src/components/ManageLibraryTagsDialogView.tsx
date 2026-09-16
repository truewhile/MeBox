import { useEffect, useMemo, useState } from 'react'
import { Check, Loader2, Pencil, Plus, Tag, Trash2, X } from 'lucide-react'
import toast from 'react-hot-toast'

import { confirmAction } from './confirmAction'
import type { Library } from '../types'
import { MAX_LIBRARY_TAGS, normalizeTagName, tagNameError, type LibraryTag } from '../utils/libraryTags'

export type ManageLibraryTagsDialogProps = {
  tags: LibraryTag[]
  libraries: Library[]
  saving: boolean
  onCreate: (name: string) => Promise<string>
  onRename: (from: string, to: string) => Promise<void>
  onRemove: (name: string) => Promise<void>
  onAssign: (libraryId: string, tagName: string) => Promise<void>
  onClose: () => void
}

/**
 * 标签管理对话框：左侧维护标签（新建 / 重命名 / 删除），右侧把每个媒体库归入
 * 某个标签。一个媒体库同时只属于一个标签，未选择即为「未分类」。
 */
export function ManageLibraryTagsDialogView({
  tags,
  libraries,
  saving,
  onCreate,
  onRename,
  onRemove,
  onAssign,
  onClose,
}: ManageLibraryTagsDialogProps) {
  const [draftName, setDraftName] = useState('')
  const [renaming, setRenaming] = useState('')
  const [renameDraft, setRenameDraft] = useState('')
  const [busyLibrary, setBusyLibrary] = useState('')

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

  return (
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black/35 p-2 backdrop-blur-sm sm:p-4"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="flex h-[calc(100dvh-1rem)] w-full max-w-4xl flex-col overflow-hidden rounded-2xl border border-white/70 bg-[var(--app-panel)] shadow-2xl sm:h-[80vh] sm:rounded-3xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-4 py-3 sm:px-6 sm:py-4">
          <div className="flex items-center gap-2">
            <Tag size={18} className="text-brand-500" />
            <h3 className="font-display text-base font-bold text-[var(--app-text)] sm:text-lg">管理标签</h3>
            {saving && <Loader2 size={14} className="animate-spin text-[var(--app-muted)]" />}
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

        <div className="grid flex-1 grid-cols-1 gap-4 overflow-hidden p-3 sm:p-6 md:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]">
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
                disabled={saving || !draftName.trim()}
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
                tags.map((tag) => (
                  <div
                    key={tag.name}
                    className="flex items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-2.5 py-2"
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
                        <span
                          className="min-w-0 flex-1 truncate text-sm font-bold text-[var(--app-text)]"
                          title={tag.name}
                        >
                          {tag.name}
                        </span>
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
                ))
              )}
            </div>
          </section>

          <section className="flex min-h-0 flex-col gap-3 border-t border-[var(--app-border)] pt-4 md:border-l md:border-t-0 md:pl-4 md:pt-0">
            <h4 className="text-xs font-bold uppercase tracking-widest text-[var(--app-muted)]">
              媒体库归类（{libraries.length}）
            </h4>
            <div className="min-h-0 flex-1 space-y-1.5 overflow-y-auto pr-1">
              {libraries.length === 0 ? (
                <p className="rounded-xl border border-dashed border-[var(--app-border)] px-3 py-6 text-center text-xs text-[var(--app-muted)]">
                  暂无可归类的媒体库。
                </p>
              ) : (
                libraries.map((library) => (
                  <div
                    key={library.id}
                    className="flex items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-2.5 py-2"
                  >
                    <span
                      className="min-w-0 flex-1 truncate text-xs font-semibold text-[var(--app-text)]"
                      title={library.name}
                    >
                      {library.name}
                    </span>
                    {busyLibrary === library.id && (
                      <Loader2 size={13} className="shrink-0 animate-spin text-[var(--app-muted)]" />
                    )}
                    <select
                      value={tagByLibrary.get(library.id) ?? ''}
                      disabled={saving || busyLibrary === library.id}
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
                ))
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
