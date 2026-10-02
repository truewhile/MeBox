import type { BookGroup } from '../api/reader'

// 书架分组的纯逻辑（对齐影视模块的 utils/libraryTags.ts）。
//
// 结构同构：组名 → 成员 ID 列表，「一本书只归一个组」，整份替换。
// 差别在于书架多两个内置页签：全部 / 未分组（未分组不持久化，只是「没归任何组」的视图）。

/** 与后端 model.MaxBookGroupNameLen 保持一致。 */
export const MAX_BOOK_GROUP_NAME_LENGTH = 24
/** 与后端 model.MaxBookGroups 保持一致。 */
export const MAX_BOOK_GROUPS = 50

/** 「全部」伪页签：不参与持久化，仅用于分组栏选中项。 */
export const ALL_GROUP_ID = '__all__'
/** 「未分组」伪页签：没有归入任何分组的书籍。 */
export const UNGROUPED_GROUP_ID = '__ungrouped__'

export type BookGroupTab = {
  /** 选中的稳定标识：ALL_GROUP_ID / UNGROUPED_GROUP_ID / 分组名。 */
  id: string
  name: string
  count: number
  kind: 'all' | 'ungrouped' | 'group'
}

const STORAGE_KEY = 'mebox_reader_book_groups'
const SELECTED_KEY = 'mebox_reader_book_group_selected'

/** 分组名去空白并按字符数截断，与后端清洗规则一致。 */
export function normalizeBookGroupName(name: string): string {
  return Array.from(name.trim()).slice(0, MAX_BOOK_GROUP_NAME_LENGTH).join('').trim()
}

export function normalizeBookGroups(raw: unknown): BookGroup[] {
  if (!Array.isArray(raw)) return []
  const out: BookGroup[] = []
  const indexByName = new Map<string, number>()
  for (const item of raw) {
    if (!item || typeof item !== 'object') continue
    const name = normalizeBookGroupName(String((item as BookGroup).name ?? ''))
    if (!name) continue
    const key = name.toLowerCase()
    let pos = indexByName.get(key)
    if (pos === undefined) {
      if (out.length >= MAX_BOOK_GROUPS) break
      out.push({ name, book_ids: [] })
      pos = out.length - 1
      indexByName.set(key, pos)
    }
    const ids = (item as BookGroup).book_ids
    if (!Array.isArray(ids)) continue
    const seen = new Set(out[pos].book_ids)
    for (const id of ids) {
      const trimmed = String(id ?? '').trim()
      if (!trimmed || seen.has(trimmed)) continue
      seen.add(trimmed)
      out[pos].book_ids.push(trimmed)
    }
  }
  return out
}

/** 读取本地兜底缓存：接口不可用时分组栏仍然可用。 */
export function readCachedBookGroups(): BookGroup[] {
  if (typeof window === 'undefined') return []
  try {
    return normalizeBookGroups(JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? '[]'))
  } catch {
    return []
  }
}

export function writeCachedBookGroups(groups: BookGroup[]): void {
  if (typeof window === 'undefined') return
  try {
    if (groups.length === 0) {
      window.localStorage.removeItem(STORAGE_KEY)
      return
    }
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(groups))
  } catch {
    // 私密模式等场景忽略存储失败。
  }
}

export function readSelectedGroupId(): string {
  if (typeof window === 'undefined') return ALL_GROUP_ID
  try {
    return window.localStorage.getItem(SELECTED_KEY)?.trim() || ALL_GROUP_ID
  } catch {
    return ALL_GROUP_ID
  }
}

export function writeSelectedGroupId(groupId: string): void {
  if (typeof window === 'undefined') return
  try {
    if (!groupId || groupId === ALL_GROUP_ID) {
      window.localStorage.removeItem(SELECTED_KEY)
      return
    }
    window.localStorage.setItem(SELECTED_KEY, groupId)
  } catch {
    // 同上。
  }
}

export async function loadBookGroups(): Promise<BookGroup[]> {
  const { readerAPI } = await import('../api/reader')
  const remote = normalizeBookGroups(await readerAPI.getBookGroups())
  writeCachedBookGroups(remote)
  return remote
}

export async function saveBookGroups(groups: BookGroup[]): Promise<BookGroup[]> {
  const { readerAPI } = await import('../api/reader')
  const saved = normalizeBookGroups(await readerAPI.setBookGroups(normalizeBookGroups(groups)))
  writeCachedBookGroups(saved)
  return saved
}

/**
 * 收敛为「一本书只归一个组」：越靠前的分组优先，后面重复出现的书会被移除。
 * 与后端 dedupeBookGroups 的语义一致。
 */
export function dedupeBookGroups(groups: BookGroup[]): BookGroup[] {
  const claimed = new Set<string>()
  return groups.map((group) => {
    const bookIds: string[] = []
    for (const id of group.book_ids) {
      if (claimed.has(id)) continue
      claimed.add(id)
      bookIds.push(id)
    }
    return { ...group, book_ids: bookIds }
  })
}

/** 分组名是否可用：非空、未超长、且（除自身外）没有重名。 */
export function bookGroupNameError(name: string, groups: BookGroup[], exceptName?: string): string {
  const trimmed = normalizeBookGroupName(name)
  if (!trimmed) return '分组名不能为空'
  if (Array.from(name.trim()).length > MAX_BOOK_GROUP_NAME_LENGTH) {
    return `分组名最多 ${MAX_BOOK_GROUP_NAME_LENGTH} 个字`
  }
  const key = trimmed.toLowerCase()
  const selfKey = exceptName ? normalizeBookGroupName(exceptName).toLowerCase() : ''
  if (key !== selfKey && groups.some((group) => group.name.toLowerCase() === key)) {
    return '分组名已存在'
  }
  return ''
}

export function findBookGroupIdByName(groups: BookGroup[], name: string): string {
  const key = normalizeBookGroupName(name).toLowerCase()
  if (!key) return ''
  return groups.find((group) => group.name.toLowerCase() === key)?.name ?? ''
}

export function groupBookIds(groups: BookGroup[], groupId: string): string[] {
  if (!groupId || groupId === ALL_GROUP_ID || groupId === UNGROUPED_GROUP_ID) return []
  const group = groups.find((item) => item.name === groupId)
  return group ? group.book_ids : []
}

/** 一本书所属的分组名；未归组返回空串。 */
export function groupIdOfBook(groups: BookGroup[], bookId: string): string {
  for (const group of groups) {
    if (group.book_ids.includes(bookId)) return group.name
  }
  return ''
}

export function isBookInGroup(groups: BookGroup[], groupId: string, bookId: string): boolean {
  return groupBookIds(groups, groupId).includes(bookId)
}

/** 所有已被某个分组认领的书籍 ID。 */
export function groupedBookIds(groups: BookGroup[]): Set<string> {
  const out = new Set<string>()
  for (const group of groups) {
    for (const id of group.book_ids) out.add(id)
  }
  return out
}

/**
 * 按分组过滤书籍：`全部` 原样返回，`未分组` 返回没有被任何分组认领的书。
 * 只做成员筛选、保持输入顺序；展示顺序由调用方决定。
 */
export function filterBooksByGroup<T extends { id: string }>(
  books: T[],
  groups: BookGroup[],
  groupId: string,
): T[] {
  if (!groupId || groupId === ALL_GROUP_ID) return books
  if (groupId === UNGROUPED_GROUP_ID) {
    const claimed = groupedBookIds(groups)
    return books.filter((book) => !claimed.has(book.id))
  }
  const ids = new Set(groupBookIds(groups, groupId))
  if (ids.size === 0) return []
  return books.filter((book) => ids.has(book.id))
}

/**
 * 生成分组栏模型：首项固定「全部」，有分组时追加「未分组」，其后按分组顺序追加。
 * 计数只统计当前书架上仍存在的书，避免分组里残留的死 ID 让数字虚高。
 */
export function buildBookGroupTabs(books: { id: string }[], groups: BookGroup[]): BookGroupTab[] {
  const available = new Set(books.map((book) => book.id))
  const claimed = new Set<string>()
  for (const group of groups) {
    for (const id of group.book_ids) {
      if (available.has(id)) claimed.add(id)
    }
  }
  const tabs: BookGroupTab[] = [
    { id: ALL_GROUP_ID, name: '全部', count: books.length, kind: 'all' },
  ]
  if (groups.length === 0) return tabs
  tabs.push({
    id: UNGROUPED_GROUP_ID,
    name: '未分组',
    count: books.length - claimed.size,
    kind: 'ungrouped',
  })
  for (const group of groups) {
    tabs.push({
      id: group.name,
      name: group.name,
      count: group.book_ids.filter((id) => available.has(id)).length,
      kind: 'group',
    })
  }
  return tabs
}

/**
 * 按给定分组名顺序重排：`orderedNames` 是期望顺序（通常来自拖拽结果）。
 * 未出现在顺序里的分组保持原相对顺序并追加到末尾，绝不丢失。
 */
export function reorderBookGroups(groups: BookGroup[], orderedNames: string[]): BookGroup[] {
  if (groups.length < 2) return groups
  const rank = new Map(orderedNames.map((name, index) => [name, index]))
  const ranked: BookGroup[] = []
  const rest: BookGroup[] = []
  for (const group of groups) {
    ;(rank.has(group.name) ? ranked : rest).push(group)
  }
  ranked.sort((a, b) => (rank.get(a.name) ?? 0) - (rank.get(b.name) ?? 0))
  return [...ranked, ...rest]
}

/** 选中的分组是否仍存在；被删除时回落到「全部」。 */
export function resolveSelectedGroupId(groups: BookGroup[], selectedId: string): string {
  if (!selectedId || selectedId === ALL_GROUP_ID || selectedId === UNGROUPED_GROUP_ID) return selectedId || ALL_GROUP_ID
  return groups.some((group) => group.name === selectedId) ? selectedId : ALL_GROUP_ID
}

/** 把一本书挂到分组下：先从原分组移除，保证一书一组。 */
export function attachBookToGroup(groups: BookGroup[], bookId: string, groupName: string): BookGroup[] {
  const target = findBookGroupIdByName(groups, groupName)
  if (!target) return groups
  const next = groups.map((group) => ({
    ...group,
    book_ids: group.book_ids.filter((id) => id !== bookId),
  }))
  return next.map((group) =>
    group.name === target ? { ...group, book_ids: [...group.book_ids, bookId] } : group,
  )
}

/** 批量把书籍挂到分组下；整批只产生一次写入。 */
export function attachBooksToGroup(groups: BookGroup[], bookIds: string[], groupName: string): BookGroup[] {
  const target = findBookGroupIdByName(groups, groupName)
  const moving = normalizeBookIds(bookIds)
  if (!target || moving.length === 0) return groups
  const movingSet = new Set(moving)
  const next = groups.map((group) => ({
    ...group,
    book_ids: group.book_ids.filter((id) => !movingSet.has(id)),
  }))
  return next.map((group) =>
    group.name === target ? { ...group, book_ids: [...group.book_ids, ...moving] } : group,
  )
}

/** 批量把书籍移出所有分组（即变为「未分组」）。 */
export function detachBooksFromGroup(groups: BookGroup[], bookIds: string[]): BookGroup[] {
  const moving = new Set(normalizeBookIds(bookIds))
  if (moving.size === 0) return groups
  return groups.map((group) => ({
    ...group,
    book_ids: group.book_ids.filter((id) => !moving.has(id)),
  }))
}

/** 批量归类：groupName 为空表示移出所有分组。 */
export function assignBooksToGroup(groups: BookGroup[], bookIds: string[], groupName: string): BookGroup[] {
  return groupName ? attachBooksToGroup(groups, bookIds, groupName) : detachBooksFromGroup(groups, bookIds)
}

/** 去掉空白与重复，保留首次出现的顺序。 */
export function normalizeBookIds(bookIds: unknown): string[] {
  if (!Array.isArray(bookIds)) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of bookIds) {
    const trimmed = String(raw ?? '').trim()
    if (!trimmed || seen.has(trimmed)) continue
    seen.add(trimmed)
    out.push(trimmed)
  }
  return out
}
