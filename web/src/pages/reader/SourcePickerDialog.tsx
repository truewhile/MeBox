import { BookOpen, Check, Loader2, X } from 'lucide-react'

import type { ReaderSearchOrigin } from '../../api/reader'

/**
 * SourcePickerDialog 换源弹窗：列出同一本书可用的书源，选中即切换。
 *
 * 书源对同一本书的封面 / 最新章节等覆盖能力不同，所以列表里带上各源的最新章节，
 * 方便挑一个更全的源；当前正在使用的源会标注「当前」且不可重复选择。
 */
export function SourcePickerDialog({
  title,
  origins,
  current,
  loading = false,
  emptyHint = '没有找到其它书源',
  onPick,
  onClose,
}: {
  title: string
  origins: ReaderSearchOrigin[]
  /** 当前正在使用的源：优先按书本地址匹配（同源可能承载多本书），其次按书源地址 */
  current?: { originURL?: string; bookURL?: string }
  loading?: boolean
  emptyHint?: string
  onPick: (origin: ReaderSearchOrigin) => void
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
        className="flex max-h-[80vh] w-full max-w-lg flex-col overflow-hidden rounded-3xl border border-[var(--app-border)] bg-[var(--app-panel)] shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-5 py-4">
          <div className="min-w-0">
            <h3 className="truncate text-sm font-bold text-[var(--app-text)]">换源 · {title}</h3>
            <p className="mt-0.5 text-2xs text-[var(--app-muted)]">
              {loading ? '正在搜索其它书源…' : `共 ${origins.length} 个可用书源 · 阅读进度会保留`}
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

        <div className="min-h-[160px] flex-1 overflow-y-auto p-2">
          {loading ? (
            <div className="flex justify-center py-12 text-[var(--app-muted)]">
              <Loader2 className="animate-spin" size={20} />
            </div>
          ) : origins.length === 0 ? (
            <p className="py-12 text-center text-xs text-[var(--app-muted)]">{emptyHint}</p>
          ) : (
            <div className="space-y-1">
              {origins.map((origin) => {
                const isCurrent =
                  (!!current?.bookURL && origin.book_url === current.bookURL) ||
                  (!current?.bookURL && !!current?.originURL && origin.origin === current.originURL)
                return (
                  <button
                    key={`${origin.source_id}|${origin.origin}|${origin.book_url}`}
                    type="button"
                    disabled={isCurrent}
                    onClick={() => onPick(origin)}
                    className="flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left transition hover:bg-[var(--app-hover)] disabled:opacity-60"
                  >
                    <BookOpen size={15} className="shrink-0 text-brand-500" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-xs font-bold text-[var(--app-text)]">
                        {origin.origin_name || origin.origin}
                      </span>
                      <span className="block truncate text-2xs text-[var(--app-muted)]">
                        {origin.latest_chapter
                          ? `最新：${origin.latest_chapter}`
                          : `书源：${origin.origin || origin.source_id}`}
                      </span>
                    </span>
                    {isCurrent ? (
                      <span className="flex shrink-0 items-center gap-1 text-2xs font-bold text-brand-600">
                        <Check size={12} /> 当前
                      </span>
                    ) : (
                      <span className="shrink-0 text-2xs text-brand-600">切换</span>
                    )}
                  </button>
                )
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
