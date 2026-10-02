import { Library, ListFilter } from 'lucide-react'

import type { BookGroupTab } from '../utils/readerBookGroups'

/**
 * 书架分组栏（对齐影视模块的 LibraryTagBar）：
 * 固定「全部」+（有分组时）「未分组」+ 每个分组一个页签，选中后只展示该分组的书。
 *
 * 只是展示层过滤，不改动书架数据本身。这里只负责展示与选中：新建 / 重命名 / 删除 /
 * 拖拽排序都在「管理」菜单的「管理分组」弹窗里做，分组栏保持干净。
 */
export function BookGroupBar({
  tabs,
  selectedGroupId,
  onSelect,
}: {
  tabs: BookGroupTab[]
  selectedGroupId: string
  onSelect: (groupId: string) => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 border-b border-[var(--app-border)] pb-3">
      {tabs.map((tab) => {
        const active = tab.id === selectedGroupId
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
            title={
              tab.kind === 'all'
                ? '显示全部书籍'
                : tab.kind === 'ungrouped'
                  ? '显示没有归入任何分组的书籍'
                  : `只看「${tab.name}」分组`
            }
          >
            {tab.kind === 'all' && <Library size={13} />}
            {tab.kind === 'ungrouped' && <ListFilter size={13} />}
            <span className="max-w-[8rem] truncate">{tab.name}</span>
            <span className={`font-mono text-[10px] ${active ? 'text-brand-500' : 'text-[var(--app-muted)]'}`}>
              {tab.count}
            </span>
          </button>
        )
      })}
    </div>
  )
}
