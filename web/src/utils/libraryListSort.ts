import type { Library } from '../types'

export type LibraryListSortField = 'name' | 'sort_order'
export type LibraryListSortOrder = 'asc' | 'desc'

export type LibraryListSortOption = {
  id: LibraryListSortField
  label: string
  defaultOrder: LibraryListSortOrder
}

export const LIBRARY_LIST_SORT_OPTIONS: LibraryListSortOption[] = [
  { id: 'name', label: '库名', defaultOrder: 'desc' },
  { id: 'sort_order', label: '手动顺序', defaultOrder: 'asc' },
]

export const DEFAULT_LIBRARY_LIST_SORT_FIELD: LibraryListSortField = 'name'
export const DEFAULT_LIBRARY_LIST_SORT_ORDER: LibraryListSortOrder = 'desc'

const FIELD_KEY = 'mebox_libraries_sort_field'
const ORDER_KEY = 'mebox_libraries_sort_order'

export function readLibraryListSort(): { field: LibraryListSortField; order: LibraryListSortOrder } {
  if (typeof window === 'undefined') {
    return { field: DEFAULT_LIBRARY_LIST_SORT_FIELD, order: DEFAULT_LIBRARY_LIST_SORT_ORDER }
  }
  const rawField = window.localStorage.getItem(FIELD_KEY)
  const rawOrder = window.localStorage.getItem(ORDER_KEY)
  const field: LibraryListSortField =
    rawField === 'name' || rawField === 'sort_order' ? rawField : DEFAULT_LIBRARY_LIST_SORT_FIELD
  const order: LibraryListSortOrder =
    rawOrder === 'asc' || rawOrder === 'desc'
      ? rawOrder
      : field === 'name'
        ? DEFAULT_LIBRARY_LIST_SORT_ORDER
        : 'asc'
  return { field, order }
}

export function writeLibraryListSort(field: LibraryListSortField, order: LibraryListSortOrder): void {
  if (typeof window === 'undefined') return
  window.localStorage.setItem(FIELD_KEY, field)
  window.localStorage.setItem(ORDER_KEY, order)
}

function compareNames(a: string, b: string, order: LibraryListSortOrder): number {
  const cmp = a.localeCompare(b, 'zh-CN', { numeric: true, sensitivity: 'base' })
  return order === 'asc' ? cmp : -cmp
}

function compareSortOrder(a: number, b: number, order: LibraryListSortOrder): number {
  const cmp = a - b
  return order === 'asc' ? cmp : -cmp
}

function librarySortKey(library: Library): { name: string; sortOrder: number } {
  return {
    name: (library.name || '').trim(),
    sortOrder: library.sort_order ?? 0,
  }
}

function compareLibraries(a: Library, b: Library, field: LibraryListSortField, order: LibraryListSortOrder): number {
  const ka = librarySortKey(a)
  const kb = librarySortKey(b)
  if (field === 'name') {
    const byName = compareNames(ka.name, kb.name, order)
    if (byName !== 0) return byName
    return compareSortOrder(ka.sortOrder, kb.sortOrder, 'asc') || a.id.localeCompare(b.id)
  }
  const byOrder = compareSortOrder(ka.sortOrder, kb.sortOrder, order)
  if (byOrder !== 0) return byOrder
  return compareNames(ka.name, kb.name, 'asc') || a.id.localeCompare(b.id)
}

/**
 * 置顶组始终在前；组内按 field/order 排序。
 * pinnedIds 的相对顺序只决定「是否置顶」，组内不再保留置顶拖拽序。
 */
export function sortLibraryPreviewsByField<T extends { library: Library }>(
  items: T[],
  pinnedIds: string[],
  field: LibraryListSortField,
  order: LibraryListSortOrder,
): T[] {
  if (items.length < 2) return items

  const pinnedRank = new Map(pinnedIds.map((id, index) => [id, index]))
  const pinned: T[] = []
  const rest: T[] = []
  for (const item of items) {
    if (pinnedRank.has(item.library.id)) pinned.push(item)
    else rest.push(item)
  }

  const byField = (a: T, b: T) => compareLibraries(a.library, b.library, field, order)
  pinned.sort(byField)
  rest.sort(byField)
  return [...pinned, ...rest]
}
