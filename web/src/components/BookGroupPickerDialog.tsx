import { Check, FolderInput, Layers, X } from 'lucide-react'

import type { BookGroup, ReaderBook } from '../api/reader'

/**
 * 「移到分组」选择框：给一本书指定所属分组。
 *
 * 书籍归组没有放在分组管理弹窗里 —— 书架可能有上千本，逐个列出来既慢又难找，
 * 直接在卡片上点「分组」更顺手。一本书只归一个分组，选「未分组」即移出所有分组。
 */
export function BookGroupPickerDialog({
  book,
  groups,
  currentGroupId,
  busy = false,
  onPick,
  onClose,
}: {
  book: ReaderBook
  groups: BookGroup[]
  /** 当前所属分组名；空串表示未分组。 */
  currentGroupId: string
  busy?: boolean
  onPick: (groupName: string) => void
  onClose: () => void
}) {
  return (
    <div
      className="fixed inset-0 z-[110] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="移到分组"
        className="flex max-h-[80vh] w-full max-w-sm flex-col overflow-hidden rounded-3xl border border-[var(--app-border)] bg-[var(--app-panel)] shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-5 py-4">
          <div className="min-w-0">
            <h3 className="truncate text-sm font-bold text-[var(--app-text)]">移到分组</h3>
            <p className="mt-0.5 truncate text-2xs text-[var(--app-muted)]">《{book.name}》</p>
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

        <div className="flex-1 overflow-y-auto p-2">
          <button
            type="button"
            disabled={busy}
            onClick={() => onPick('')}
            className="flex w-full items-center gap-2 rounded-xl px-3 py-2.5 text-left transition hover:bg-[var(--app-hover)] disabled:opacity-50"
          >
            <Layers size={14} className="shrink-0 text-[var(--app-muted)]" />
            <span className="min-w-0 flex-1 truncate text-xs font-bold text-[var(--app-text)]">未分组</span>
            {currentGroupId === '' && <Check size={14} className="shrink-0 text-brand-600" />}
          </button>

          {groups.length === 0 ? (
            <p className="px-3 py-6 text-center text-2xs text-[var(--app-muted)]">
              还没有分组，先在书架分组栏的「管理分组」里新建一个
            </p>
          ) : (
            groups.map((group) => (
              <button
                key={group.name}
                type="button"
                disabled={busy}
                onClick={() => onPick(group.name)}
                className="flex w-full items-center gap-2 rounded-xl px-3 py-2.5 text-left transition hover:bg-[var(--app-hover)] disabled:opacity-50"
              >
                <FolderInput size={14} className="shrink-0 text-brand-500" />
                <span className="min-w-0 flex-1 truncate text-xs font-bold text-[var(--app-text)]">
                  {group.name}
                </span>
                {currentGroupId === group.name && <Check size={14} className="shrink-0 text-brand-600" />}
              </button>
            ))
          )}
        </div>
      </div>
    </div>
  )
}
