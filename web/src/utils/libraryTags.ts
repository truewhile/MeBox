import type { Library, LibraryTagSet } from '../types'

/** 与后端 model.MaxLibraryTagNameLen 保持一致。 */
export const MAX_TAG_NAME_LENGTH = 24
/** 与后端 model.MaxLibraryTags 保持一致。 */
export const MAX_LIBRARY_TAGS = 50

/** 「全部」伪标签：不参与持久化，仅用于标签栏选中项。 */
export const ALL_TAG_ID = '__all__'

export type LibraryTag = LibraryTagSet

export type LibraryTagTab = {
  /** 选中的稳定标识：ALL_TAG_ID 或标签名。 */
  id: string
  name: string
  count: number
  /** 是否为「全部」标签栏 */
  isAll: boolean
}

const STORAGE_KEY = 'mebox_library_tags'
const SELECTED_KEY = 'mebox_library_tag_selected'

/** 标签名去空白并按字符数截断，与后端清洗规则一致。 */
export function normalizeTagName(name: string): string {
  return Array.from(name.trim()).slice(0, MAX_TAG_NAME_LENGTH).join('').trim()
}

export function normalizeLibraryTags(tags: unknown): LibraryTag[] {
  if (!Array.isArray(tags)) return []
  const out: LibraryTag[] = []
  const indexByName = new Map<string, number>()
  for (const raw of tags) {
    if (!raw || typeof raw !== 'object') continue
    const name = normalizeTagName(String((raw as LibraryTagSet).name ?? ''))
    if (!name) continue
    const key = name.toLowerCase()
    let pos = indexByName.get(key)
    if (pos === undefined) {
      if (out.length >= MAX_LIBRARY_TAGS) break
      out.push({ name, library_ids: [] })
      pos = out.length - 1
      indexByName.set(key, pos)
    }
    const ids = (raw as LibraryTagSet).library_ids
    if (!Array.isArray(ids)) continue
    const seen = new Set(out[pos].library_ids)
    for (const id of ids) {
      const trimmed = String(id ?? '').trim()
      if (!trimmed || seen.has(trimmed)) continue
      seen.add(trimmed)
      out[pos].library_ids.push(trimmed)
    }
  }
  return out
}

/** 读取本地兜底缓存：接口不可用时标签栏仍然可用。 */
export function readCachedLibraryTags(): LibraryTag[] {
  if (typeof window === 'undefined') return []
  try {
    return normalizeLibraryTags(JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? '[]'))
  } catch {
    return []
  }
}

export function writeCachedLibraryTags(tags: LibraryTag[]): void {
  if (typeof window === 'undefined') return
  try {
    if (tags.length === 0) {
      window.localStorage.removeItem(STORAGE_KEY)
      return
    }
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(tags))
  } catch {
    // 私密模式等场景忽略存储失败。
  }
}

export function readSelectedTagId(): string {
  if (typeof window === 'undefined') return ALL_TAG_ID
  try {
    return window.localStorage.getItem(SELECTED_KEY)?.trim() || ALL_TAG_ID
  } catch {
    return ALL_TAG_ID
  }
}

export function writeSelectedTagId(tagId: string): void {
  if (typeof window === 'undefined') return
  try {
    if (!tagId || tagId === ALL_TAG_ID) {
      window.localStorage.removeItem(SELECTED_KEY)
      return
    }
    window.localStorage.setItem(SELECTED_KEY, tagId)
  } catch {
    // 同上。
  }
}

export async function loadLibraryTags(): Promise<LibraryTag[]> {
  const { profileAPI } = await import('../api/profile')
  const remote = normalizeLibraryTags(await profileAPI.getLibraryTags())
  writeCachedLibraryTags(remote)
  return remote
}

export async function saveLibraryTags(tags: LibraryTag[]): Promise<LibraryTag[]> {
  const { profileAPI } = await import('../api/profile')
  const saved = normalizeLibraryTags(await profileAPI.setLibraryTags(normalizeLibraryTags(tags)))
  writeCachedLibraryTags(saved)
  return saved
}

/**
 * 把标签集合中的媒体库 ID 收敛为「每个库只归属一个标签」：越靠前的标签优先，
 * 后面的标签里重复出现的库会被移除。与后端 SetLibraryTags 的语义一致。
 */
export function dedupeLibraryTags(tags: LibraryTag[]): LibraryTag[] {
  const claimed = new Set<string>()
  return tags.map((tag) => {
    const libraryIds: string[] = []
    for (const id of tag.library_ids) {
      if (claimed.has(id)) continue
      claimed.add(id)
      libraryIds.push(id)
    }
    return { ...tag, library_ids: libraryIds }
  })
}

/** 标签名是否可用：非空、未超长、且（除了自身以外）没有重名。 */
export function tagNameError(name: string, tags: LibraryTag[], exceptName?: string): string {
  const trimmed = normalizeTagName(name)
  if (!trimmed) return '标签名不能为空'
  if (Array.from(name.trim()).length > MAX_TAG_NAME_LENGTH) {
    return `标签名最多 ${MAX_TAG_NAME_LENGTH} 个字`
  }
  const key = trimmed.toLowerCase()
  const selfKey = exceptName ? normalizeTagName(exceptName).toLowerCase() : ''
  if (key !== selfKey && tags.some((tag) => tag.name.toLowerCase() === key)) {
    return '标签名已存在'
  }
  return ''
}

export function findTagIdByName(tags: LibraryTag[], name: string): string {
  const key = normalizeTagName(name).toLowerCase()
  if (!key) return ''
  return tags.find((tag) => tag.name.toLowerCase() === key)?.name ?? ''
}

export function tagLibraryIds(tags: LibraryTag[], tagId: string): string[] {
  if (!tagId || tagId === ALL_TAG_ID) return []
  const tag = tags.find((item) => item.name === tagId)
  return tag ? tag.library_ids : []
}

export function isLibraryInTag(tags: LibraryTag[], tagId: string, libraryId: string): boolean {
  return tagLibraryIds(tags, tagId).includes(libraryId)
}

/**
 * 按标签过滤媒体库：`tagId` 为「全部」时原样返回。
 * 只做成员筛选、保持输入顺序，展示顺序（置顶、排序下拉等）由调用方决定；
 * 标签里记录的 library_ids 顺序只表示归属，不作为展示排序。
 */
export function filterLibrariesByTag<T extends { id: string }>(
  libraries: T[],
  tags: LibraryTag[],
  tagId: string,
): T[] {
  if (!tagId || tagId === ALL_TAG_ID) return libraries
  const ids = new Set(tagLibraryIds(tags, tagId))
  if (ids.size === 0) return []
  return libraries.filter((lib) => ids.has(lib.id))
}

/** 生成标签栏模型：首项固定为「全部」，其后按标签顺序追加。 */
export function buildLibraryTagTabs(libraries: Library[], tags: LibraryTag[]): LibraryTagTab[] {
  const tabs: LibraryTagTab[] = [
    { id: ALL_TAG_ID, name: '全部', count: libraries.length, isAll: true },
  ]
  if (tags.length === 0) return tabs
  const available = new Set(libraries.map((lib) => lib.id))
  for (const tag of tags) {
    tabs.push({
      id: tag.name,
      name: tag.name,
      count: tag.library_ids.filter((id) => available.has(id)).length,
      isAll: false,
    })
  }
  return tabs
}

/**
 * 按给定标签名顺序重排标签栏：`orderedNames` 是期望的标签名顺序（通常来自拖拽后的顺序）。
 * 未出现在顺序里的标签（例如新建、旧数据并发写入）保持原相对顺序，追加到末尾，绝不丢失。
 */
export function reorderLibraryTags(tags: LibraryTag[], orderedNames: string[]): LibraryTag[] {
  if (tags.length < 2) return tags
  const rank = new Map(orderedNames.map((name, index) => [name, index]))
  const ranked: LibraryTag[] = []
  const rest: LibraryTag[] = []
  for (const tag of tags) {
    ;(rank.has(tag.name) ? ranked : rest).push(tag)
  }
  ranked.sort((a, b) => (rank.get(a.name) ?? 0) - (rank.get(b.name) ?? 0))
  return [...ranked, ...rest]
}

/** 选中的标签是否仍存在；被删除或被过滤掉时回落到「全部」。 */
export function resolveSelectedTagId(tags: LibraryTag[], selectedId: string): string {
  if (!selectedId || selectedId === ALL_TAG_ID) return ALL_TAG_ID
  return tags.some((tag) => tag.name === selectedId) ? selectedId : ALL_TAG_ID
}

/** 标签下的媒体库条目总数（用于标题文案）。 */
export function sumLibraryTotals(libraries: Library[]): number {
  return libraries.reduce((sum, lib) => sum + (lib.total ?? 0), 0)
}

/** 把一个媒体库挂到标签下：会先从原标签移除，保证一库一标签。 */
export function attachLibraryToTag(tags: LibraryTag[], libraryId: string, tagName: string): LibraryTag[] {
  const target = findTagIdByName(tags, tagName)
  if (!target) return tags
  const next = tags.map((tag) => ({
    ...tag,
    library_ids: tag.library_ids.filter((id) => id !== libraryId),
  }))
  return next.map((tag) =>
    tag.name === target ? { ...tag, library_ids: [...tag.library_ids, libraryId] } : tag,
  )
}

/** 从标签下移除媒体库。 */
export function detachLibraryFromTag(tags: LibraryTag[], libraryId: string, tagName: string): LibraryTag[] {
  return tags.map((tag) =>
    tag.name === tagName ? { ...tag, library_ids: tag.library_ids.filter((id) => id !== libraryId) } : tag,
  )
}

/**
 * 批量把一个或多个媒体库挂到标签下：先从各自原标签移除，再按传入顺序追加，
 * 保证「一库一标签」。整批只产生一次写入。
 */
export function attachLibrariesToTag(
  tags: LibraryTag[],
  libraryIds: string[],
  tagName: string,
): LibraryTag[] {
  const target = findTagIdByName(tags, tagName)
  const moving = normalizeLibraryIds(libraryIds)
  if (!target || moving.length === 0) return tags
  const movingSet = new Set(moving)
  const next = tags.map((tag) => ({
    ...tag,
    library_ids: tag.library_ids.filter((id) => !movingSet.has(id)),
  }))
  return next.map((tag) =>
    tag.name === target ? { ...tag, library_ids: [...tag.library_ids, ...moving] } : tag,
  )
}

/** 批量把媒体库移出所有标签（即变为「未分类」）。 */
export function detachLibrariesFromTag(tags: LibraryTag[], libraryIds: string[]): LibraryTag[] {
  const moving = new Set(normalizeLibraryIds(libraryIds))
  if (moving.size === 0) return tags
  return tags.map((tag) => ({
    ...tag,
    library_ids: tag.library_ids.filter((id) => !moving.has(id)),
  }))
}

/** 批量归类：tagName 为空表示移出所有标签。 */
export function assignLibrariesToTag(
  tags: LibraryTag[],
  libraryIds: string[],
  tagName: string,
): LibraryTag[] {
  return tagName ? attachLibrariesToTag(tags, libraryIds, tagName) : detachLibrariesFromTag(tags, libraryIds)
}

/** 去掉空白与重复，保留首次出现的顺序。 */
export function normalizeLibraryIds(libraryIds: unknown): string[] {
  if (!Array.isArray(libraryIds)) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of libraryIds) {
    const trimmed = String(raw ?? '').trim()
    if (!trimmed || seen.has(trimmed)) continue
    seen.add(trimmed)
    out.push(trimmed)
  }
  return out
}

/** 媒体库列表的模糊匹配（名称 / 路径 / 类型），供标签管理里的搜索框使用。 */
export function filterLibrariesForTagging(libraries: Library[], keyword: string): Library[] {
  const needle = keyword.trim().toLowerCase()
  if (!needle) return libraries
  return libraries.filter((lib) =>
    `${lib.name} ${lib.path} ${lib.type}`.toLowerCase().includes(needle),
  )
}
