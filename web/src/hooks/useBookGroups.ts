import { useCallback, useEffect, useRef, useState } from 'react'
import toast from 'react-hot-toast'

import type { BookGroup } from '../api/reader'
import {
  ALL_GROUP_ID,
  assignBooksToGroup,
  attachBookToGroup,
  dedupeBookGroups,
  detachBooksFromGroup,
  loadBookGroups,
  normalizeBookGroups,
  normalizeBookGroupName,
  readCachedBookGroups,
  readSelectedGroupId,
  reorderBookGroups,
  resolveSelectedGroupId,
  saveBookGroups,
  writeCachedBookGroups,
  writeSelectedGroupId,
} from '../utils/readerBookGroups'

export type UseBookGroupsResult = {
  /** 当前用户维护的分组（顺序即分组栏顺序）。 */
  groups: BookGroup[]
  /** 分组数据是否仍在首次加载中。 */
  loading: boolean
  /** 是否有写入在途（可用于禁用重复点击）。 */
  saving: boolean
  /** 接口不可用（例如旧后端）时降级为本地分组。 */
  loadError: boolean
  /** 当前选中的分组栏：ALL_GROUP_ID 表示「全部」。 */
  selectedGroupId: string
  setSelectedGroupId: (groupId: string) => void
  createGroup: (name: string) => Promise<string>
  renameGroup: (from: string, to: string) => Promise<void>
  /** 按给定分组名顺序重排分组栏（拖拽排序的落点）。 */
  reorderGroups: (orderedNames: string[]) => Promise<void>
  removeGroup: (name: string) => Promise<void>
  /** 覆盖某个分组下的书籍集合（保持传入顺序）。 */
  setGroupBooks: (name: string, bookIds: string[]) => Promise<void>
  /** 把一本书归到分组下；groupName 为空表示移出所有分组。 */
  assignBook: (bookId: string, groupName: string) => Promise<void>
  /** 批量归类；groupName 为空表示批量移出所有分组。一次写入。 */
  assignBooks: (bookIds: string[], groupName: string) => Promise<void>
}

/**
 * 书架分组的用户级读写（实现照搬影视模块的 useLibraryTags）。
 *
 * 写入采用「先乐观更新、串行提交、失败回滚」：分组是整份替换（`PUT`），
 * 并发请求可能让旧快照最后落库，因此所有写操作串行排队，每次发送的都是排队时的
 * 最新本地状态。接口不可用（旧后端 / 网络异常）时退回本地存储，分组栏依旧可用。
 */
export function useBookGroups(): UseBookGroupsResult {
  const [groups, setGroups] = useState<BookGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [selectedGroupId, setSelectedGroupIdState] = useState(() => readSelectedGroupId())

  const groupsRef = useRef<BookGroup[]>([])
  const selectedRef = useRef(selectedGroupId)
  const savedRef = useRef<BookGroup[]>([])
  const chainRef = useRef<Promise<unknown>>(Promise.resolve())
  const pendingRef = useRef(0)
  // 后端没有该接口（旧版本）时退化为仅本地存储，不再反复提示保存失败。
  const localOnlyRef = useRef(false)

  useEffect(() => {
    groupsRef.current = groups
  }, [groups])

  useEffect(() => {
    selectedRef.current = selectedGroupId
  }, [selectedGroupId])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    loadBookGroups()
      .then((rows) => {
        if (cancelled) return
        groupsRef.current = rows
        savedRef.current = rows
        setGroups(rows)
        setLoadError(false)
      })
      .catch((err) => {
        if (cancelled) return
        // 旧后端没有 /reader/book-groups：用本地缓存兜底，分组栏照常可用。
        const cached = readCachedBookGroups()
        groupsRef.current = cached
        savedRef.current = cached
        setGroups(cached)
        setLoadError(true)
        localOnlyRef.current = isMissingEndpoint(err)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  // 选中的分组可能被删除，落回「全部」以免分组栏空白。
  // 首次加载完成前 groups 为空集合，此时不能据此判定分组已被删除。
  useEffect(() => {
    if (loading) return
    const resolved = resolveSelectedGroupId(groups, selectedGroupId)
    if (resolved !== selectedGroupId) {
      selectedRef.current = resolved
      setSelectedGroupIdState(resolved)
    }
  }, [loading, groups, selectedGroupId])

  const setSelectedGroupId = useCallback((groupId: string) => {
    const next = groupId && groupId !== ALL_GROUP_ID ? groupId : ALL_GROUP_ID
    selectedRef.current = next
    writeSelectedGroupId(next)
    setSelectedGroupIdState(next)
  }, [])

  const mutate = useCallback((updater: (current: BookGroup[]) => BookGroup[]) => {
    const rollback = savedRef.current
    const next = dedupeBookGroups(normalizeBookGroups(updater(groupsRef.current)))
    groupsRef.current = next
    setGroups(next)

    pendingRef.current += 1
    setSaving(true)
    const task = chainRef.current.then(async () => {
      // 发送排队时的最新本地状态：链上更早的写入已被后面的状态取代。
      const snapshot = groupsRef.current
      if (localOnlyRef.current) {
        if (groupsRef.current === snapshot) savedRef.current = snapshot
        writeCachedBookGroups(snapshot)
        return
      }
      try {
        const saved = await saveBookGroups(snapshot)
        savedRef.current = saved
        setLoadError(false)
        if (groupsRef.current === snapshot) {
          groupsRef.current = saved
          setGroups(saved)
        }
      } catch (err) {
        if (isMissingEndpoint(err)) {
          // 旧后端不支持分组接口：本次修改保留在本地存储，分组栏照常可用。
          localOnlyRef.current = true
          savedRef.current = snapshot
          writeCachedBookGroups(snapshot)
          setLoadError(true)
          return
        }
        // 用最后一次成功保存的状态回滚，避免前端显示一份服务端并不存在的分组。
        groupsRef.current = rollback
        savedRef.current = rollback
        setGroups(rollback)
        setLoadError(true)
        toast.error('分组保存失败，请稍后重试')
      }
    })
    chainRef.current = task.catch(() => undefined)
    return task.finally(() => {
      pendingRef.current = Math.max(0, pendingRef.current - 1)
      if (pendingRef.current === 0) setSaving(false)
    })
  }, [])

  const createGroup = useCallback(
    async (name: string) => {
      const trimmed = normalizeBookGroupName(name)
      if (!trimmed) return ''
      if (groupsRef.current.some((group) => group.name.toLowerCase() === trimmed.toLowerCase())) {
        return trimmed
      }
      await mutate((current) => [...current, { name: trimmed, book_ids: [] }])
      return groupsRef.current.some((group) => group.name === trimmed) ? trimmed : ''
    },
    [mutate],
  )

  const renameGroup = useCallback(
    async (from: string, to: string) => {
      const trimmed = normalizeBookGroupName(to)
      if (!trimmed || trimmed === from) return
      await mutate((current) =>
        current.map((group) => (group.name === from ? { ...group, name: trimmed } : group)),
      )
      // 重命名后选中项要跟着换到新名字，否则分组栏会落回「全部」。
      if (selectedRef.current === from) setSelectedGroupId(trimmed)
    },
    [mutate, setSelectedGroupId],
  )

  const reorderGroups = useCallback(
    async (orderedNames: string[]) => {
      await mutate((current) => reorderBookGroups(current, orderedNames))
    },
    [mutate],
  )

  const removeGroup = useCallback(
    async (name: string) => {
      await mutate((current) => current.filter((group) => group.name !== name))
      if (selectedRef.current === name) setSelectedGroupId(ALL_GROUP_ID)
    },
    [mutate, setSelectedGroupId],
  )

  const setGroupBooks = useCallback(
    async (name: string, bookIds: string[]) => {
      await mutate((current) =>
        current.map((group) => (group.name === name ? { ...group, book_ids: bookIds } : group)),
      )
    },
    [mutate],
  )

  const assignBook = useCallback(
    async (bookId: string, groupName: string) => {
      await mutate((current) =>
        groupName ? attachBookToGroup(current, bookId, groupName) : detachBooksFromGroup(current, [bookId]),
      )
    },
    [mutate],
  )

  const assignBooks = useCallback(
    async (bookIds: string[], groupName: string) => {
      await mutate((current) => assignBooksToGroup(current, bookIds, groupName))
    },
    [mutate],
  )

  return {
    groups,
    loading,
    saving,
    loadError,
    selectedGroupId,
    setSelectedGroupId,
    createGroup,
    renameGroup,
    reorderGroups,
    removeGroup,
    setGroupBooks,
    assignBook,
    assignBooks,
  }
}

/** 判断错误是否说明后端没有这个接口（旧版本 / 反向代理未更新）。 */
function isMissingEndpoint(err: unknown): boolean {
  const status = (err as { response?: { status?: number } })?.response?.status
  return status === 404 || status === 405 || status === 501
}
