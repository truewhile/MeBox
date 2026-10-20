import { Check, X } from 'lucide-react'
import type { ReactNode } from 'react'

import {
  SHELF_GRID_COLUMNS,
  useReaderSettingsStore,
} from '../../stores/readerSettings'
import { SHELF_LAYOUT_OPTIONS, SHELF_SORT_OPTIONS, gridColumnsLabel } from './bookshelfModel'

// 书架设置弹窗（对应 legado 的 dialog_bookshelf_config）：
// 布局（列表 / 紧凑列表 / 网格 + 列数）、排序、以及显示项开关。
// 所有选项直接落到 readerSettings store，关掉弹窗即生效。

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div className="border-t border-[var(--app-border)] px-5 py-4 first:border-t-0">
      <p className="text-xs font-bold text-[var(--app-text)]">{title}</p>
      {hint && <p className="mt-0.5 text-2xs text-[var(--app-muted)]">{hint}</p>}
      <div className="mt-3">{children}</div>
    </div>
  )
}

function RadioOption({
  active,
  label,
  hint,
  onClick,
}: {
  active: boolean
  label: string
  hint?: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      onClick={onClick}
      className={`flex w-full items-center gap-2 rounded-xl border px-3 py-2 text-left transition ${
        active
          ? 'border-brand-500 bg-brand-500/10'
          : 'border-[var(--app-border)] hover:bg-[var(--app-hover)]'
      }`}
    >
      <span className="min-w-0 flex-1">
        <span className={`block truncate text-xs font-bold ${active ? 'text-brand-600' : 'text-[var(--app-text)]'}`}>
          {label}
        </span>
        {hint && <span className="block truncate text-2xs text-[var(--app-muted)]">{hint}</span>}
      </span>
      {active && <Check size={14} className="shrink-0 text-brand-600" />}
    </button>
  )
}

function ToggleOption({
  active,
  label,
  hint,
  onToggle,
}: {
  active: boolean
  label: string
  hint?: string
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={active}
      onClick={onToggle}
      className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left transition hover:bg-[var(--app-hover)]"
    >
      <span className="min-w-0 flex-1">
        <span className="block text-xs font-bold text-[var(--app-text)]">{label}</span>
        {hint && <span className="block text-2xs text-[var(--app-muted)]">{hint}</span>}
      </span>
      <span
        className={`relative h-5 w-9 shrink-0 rounded-full transition ${active ? 'bg-brand-500' : 'bg-[var(--app-border)]'}`}
      >
        <span
          className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all ${
            active ? 'left-[1.125rem]' : 'left-0.5'
          }`}
        />
      </span>
    </button>
  )
}

export function BookshelfSettingsDialog({ onClose }: { onClose: () => void }) {
  const layout = useReaderSettingsStore((s) => s.shelfLayout)
  const setLayout = useReaderSettingsStore((s) => s.setShelfLayout)
  const columns = useReaderSettingsStore((s) => s.shelfGridColumns)
  const setColumns = useReaderSettingsStore((s) => s.setShelfGridColumns)
  const sort = useReaderSettingsStore((s) => s.shelfSort)
  const setSort = useReaderSettingsStore((s) => s.setShelfSort)
  const showUnread = useReaderSettingsStore((s) => s.shelfShowUnread)
  const setShowUnread = useReaderSettingsStore((s) => s.setShelfShowUnread)
  const showUpdateTime = useReaderSettingsStore((s) => s.shelfShowUpdateTime)
  const setShowUpdateTime = useReaderSettingsStore((s) => s.setShelfShowUpdateTime)

  return (
    <div
      className="fixed inset-0 z-[110] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="书架设置"
        className="flex max-h-[85vh] w-full max-w-md flex-col overflow-hidden rounded-3xl border border-[var(--app-border)] bg-[var(--app-panel)] shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-5 py-4">
          <div>
            <h3 className="text-sm font-bold text-[var(--app-text)]">书架设置</h3>
            <p className="mt-0.5 text-2xs text-[var(--app-muted)]">布局、排序与显示项</p>
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

        <div className="flex-1 overflow-y-auto">
          <Section title="布局" hint="网格可再选列数，自适应会随屏幕宽度变化">
            <div role="radiogroup" className="grid gap-2">
              {SHELF_LAYOUT_OPTIONS.map((option) => (
                <RadioOption
                  key={option.value}
                  active={layout === option.value}
                  label={option.label}
                  hint={option.hint}
                  onClick={() => setLayout(option.value)}
                />
              ))}
            </div>
            {layout === 'grid' && (
              <div className="mt-3 flex flex-wrap items-center gap-1.5">
                <span className="mr-1 text-2xs text-[var(--app-muted)]">列数</span>
                {SHELF_GRID_COLUMNS.map((value) => (
                  <button
                    key={value}
                    type="button"
                    onClick={() => setColumns(value)}
                    className={`rounded-lg border px-2.5 py-1 text-2xs font-bold transition ${
                      columns === value
                        ? 'border-brand-500 bg-brand-500/10 text-brand-600'
                        : 'border-[var(--app-border)] text-[var(--app-muted)] hover:bg-[var(--app-hover)]'
                    }`}
                  >
                    {gridColumnsLabel(value)}
                  </button>
                ))}
              </div>
            )}
          </Section>

          <Section title="排序">
            <div role="radiogroup" className="grid grid-cols-2 gap-2">
              {SHELF_SORT_OPTIONS.map((option) => (
                <RadioOption
                  key={option.value}
                  active={sort === option.value}
                  label={option.label}
                  onClick={() => setSort(option.value)}
                />
              ))}
            </div>
          </Section>

          <Section title="显示项">
            <div className="grid gap-1">
              <ToggleOption
                active={showUnread}
                label="未读章数"
                hint="封面右上角显示还剩多少章没读"
                onToggle={() => setShowUnread(!showUnread)}
              />
              <ToggleOption
                active={showUpdateTime}
                label="更新时间"
                hint="列表布局显示最近一次检测到新章节的时间"
                onToggle={() => setShowUpdateTime(!showUpdateTime)}
              />
            </div>
          </Section>
        </div>
      </div>
    </div>
  )
}
