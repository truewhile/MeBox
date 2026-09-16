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
 *
 * 首页与 /libraries 共用这份排序，保证两处的媒体库顺序一致。
 */
export function sortLibrariesByField(
  libraries: Library[],
  pinnedIds: string[],
  field: LibraryListSortField,
  order: LibraryListSortOrder,
): Library[] {
  if (libraries.length < 2) return libraries

  const pinnedRank = new Map(pinnedIds.map((id, index) => [id, index]))
  const pinned: Library[] = []
  const rest: Library[] = []
  for (const library of libraries) {
    if (pinnedRank.has(library.id)) pinned.push(library)
    else rest.push(library)
  }

  const byField = (a: Library, b: Library) => compareLibraries(a, b, field, order)
  pinned.sort(byField)
  rest.sort(byField)
  return [...pinned, ...rest]
}

/** 预览列表按库排序：先排库，再按排好的库顺序取回对应预览。 */
export function sortLibraryPreviewsByField<T extends { library: Library }>(
  items: T[],
  pinnedIds: string[],
  field: LibraryListSortField,
  order: LibraryListSortOrder,
): T[] {
  if (items.length < 2) return items
  const ordered = sortLibrariesByField(
    items.map((item) => item.library),
    pinnedIds,
    field,
    order,
  )
  const byId = new Map(items.map((item) => [item.library.id, item]))
  return ordered
    .map((library) => byId.get(library.id))
    .filter((item): item is T => item !== undefined)
}
