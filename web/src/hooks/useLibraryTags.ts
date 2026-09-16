import { useCallback, useEffect, useRef, useState } from 'react'
import toast from 'react-hot-toast'

import {
  ALL_TAG_ID,
  attachLibraryToTag,
  dedupeLibraryTags,
  loadLibraryTags,
  normalizeLibraryTags,
  normalizeTagName,
  readCachedLibraryTags,
  readSelectedTagId,
  resolveSelectedTagId,
  saveLibraryTags,
  writeCachedLibraryTags,
  writeSelectedTagId,
  type LibraryTag,
} from '../utils/libraryTags'

export type UseLibraryTagsResult = {
  /** 当前用户维护的标签（顺序即标签栏顺序）。 */
  tags: LibraryTag[]
  /** 标签数据是否仍在首次加载中。 */
  loading: boolean
  /** 是否有写入在途（可用于禁用重复点击）。 */
  saving: boolean
  /** 接口不可用（例如旧后端）时降级为本地标签。 */
  loadError: boolean
  /** 当前选中的标签栏：ALL_TAG_ID 表示「全部」。 */
  selectedTagId: string
  setSelectedTagId: (tagId: string) => void
  createTag: (name: string) => Promise<string>
  renameTag: (from: string, to: string) => Promise<void>
  removeTag: (name: string) => Promise<void>
  /** 覆盖某个标签下的媒体库集合（保持传入顺序）。 */
  setTagLibraries: (name: string, libraryIds: string[]) => Promise<void>
  /** 把媒体库挂到标签下；tagName 为空表示移出所有标签。 */
  assignLibrary: (libraryId: string, tagName: string) => Promise<void>
}

/**
 * 媒体库标签的用户级读写。
 *
 * 写入采用「先乐观更新、串行提交、失败回滚」：标签是整份替换（`PUT`），
 * 并发请求可能让旧快照最后落库，因此所有写操作串行排队，每次发送的都是
 * 排队时的最新本地状态。接口不可用（旧后端/网络异常）时退回本地存储，
 * 标签栏依旧可用，不会因为一次失败就让标签凭空消失。
 */
export function useLibraryTags(): UseLibraryTagsResult {
  const [tags, setTags] = useState<LibraryTag[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [selectedTagId, setSelectedTagIdState] = useState(() => readSelectedTagId())

  const tagsRef = useRef<LibraryTag[]>([])
  const selectedRef = useRef(selectedTagId)
  const savedRef = useRef<LibraryTag[]>([])
  const chainRef = useRef<Promise<unknown>>(Promise.resolve())
  const pendingRef = useRef(0)
  // 后端没有该接口（旧版本）时退化为仅本地存储，不再反复提示保存失败。
  const localOnlyRef = useRef(false)

  useEffect(() => {
    tagsRef.current = tags
  }, [tags])

  useEffect(() => {
    selectedRef.current = selectedTagId
  }, [selectedTagId])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    loadLibraryTags()
      .then((rows) => {
        if (cancelled) return
        tagsRef.current = rows
        savedRef.current = rows
        setTags(rows)
        setLoadError(false)
      })
      .catch((err) => {
        if (cancelled) return
        // 旧后端没有 /me/library-tags：用本地缓存兜底，标签栏照常可用。
        const cached = readCachedLibraryTags()
        tagsRef.current = cached
        savedRef.current = cached
        setTags(cached)
        setLoadError(true)
        // 接口不存在时（旧后端）只做本地读写，避免每次编辑都弹一次保存失败。
        localOnlyRef.current = isMissingEndpoint(err)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  // 选中的标签可能被删除，落回「全部」以免标签栏空白。
  useEffect(() => {
    const resolved = resolveSelectedTagId(tags, selectedTagId)
    if (resolved !== selectedTagId) {
      selectedRef.current = resolved
      setSelectedTagIdState(resolved)
    }
  }, [tags, selectedTagId])

  const setSelectedTagId = useCallback((tagId: string) => {
    const next = tagId && tagId !== ALL_TAG_ID ? tagId : ALL_TAG_ID
    selectedRef.current = next
    writeSelectedTagId(next)
    setSelectedTagIdState(next)
  }, [])

  const mutate = useCallback((updater: (current: LibraryTag[]) => LibraryTag[]) => {
    const rollback = savedRef.current
    const next = dedupeLibraryTags(normalizeLibraryTags(updater(tagsRef.current)))
    tagsRef.current = next
    setTags(next)

    pendingRef.current += 1
    setSaving(true)
    const task = chainRef.current.then(async () => {
      // 发送排队时的最新本地状态：链上更早的写入已经被后面的状态取代。
      const snapshot = tagsRef.current
      if (localOnlyRef.current) {
        // 已知后端不支持标签接口：只落本地缓存，不再发请求。
        if (tagsRef.current === snapshot) savedRef.current = snapshot
        writeCachedLibraryTags(snapshot)
        return
      }
      try {
        const saved = await saveLibraryTags(snapshot)
        savedRef.current = saved
        setLoadError(false)
        if (tagsRef.current === snapshot) {
          tagsRef.current = saved
          setTags(saved)
        }
      } catch (err) {
        if (isMissingEndpoint(err)) {
          // 旧后端不支持标签接口：本次修改保留在本地存储，标签栏照常可用。
          localOnlyRef.current = true
          savedRef.current = snapshot
          writeCachedLibraryTags(snapshot)
          setLoadError(true)
          return
        }
        // 用最后一次成功保存的状态回滚，避免前端显示一份服务端并不存在的标签。
        tagsRef.current = rollback
        savedRef.current = rollback
        setTags(rollback)
        setLoadError(true)
        toast.error('标签保存失败，请稍后重试')
      }
    })
    chainRef.current = task.catch(() => undefined)
    return task.finally(() => {
      pendingRef.current = Math.max(0, pendingRef.current - 1)
      if (pendingRef.current === 0) setSaving(false)
    })
  }, [])

  const createTag = useCallback(
    async (name: string) => {
      const trimmed = normalizeTagName(name)
      if (!trimmed) return ''
      if (tagsRef.current.some((tag) => tag.name.toLowerCase() === trimmed.toLowerCase())) {
        return trimmed
      }
      await mutate((current) => [...current, { name: trimmed, library_ids: [] }])
      return tagsRef.current.some((tag) => tag.name === trimmed) ? trimmed : ''
    },
    [mutate],
  )

  const renameTag = useCallback(
    async (from: string, to: string) => {
      const trimmed = normalizeTagName(to)
      if (!trimmed || trimmed === from) return
      await mutate((current) =>
        current.map((tag) => (tag.name === from ? { ...tag, name: trimmed } : tag)),
      )
      // 重命名后选中项要跟着换到新名字，否则标签栏会落回「全部」。
      if (selectedRef.current === from) setSelectedTagId(trimmed)
    },
    [mutate, setSelectedTagId],
  )

  const removeTag = useCallback(
    async (name: string) => {
      await mutate((current) => current.filter((tag) => tag.name !== name))
      if (selectedRef.current === name) setSelectedTagId(ALL_TAG_ID)
    },
    [mutate, setSelectedTagId],
  )

  const setTagLibraries = useCallback(
    async (name: string, libraryIds: string[]) => {
      await mutate((current) =>
        current.map((tag) => (tag.name === name ? { ...tag, library_ids: libraryIds } : tag)),
      )
    },
    [mutate],
  )

  const assignLibrary = useCallback(
    async (libraryId: string, tagName: string) => {
      await mutate((current) => {
        if (tagName) return attachLibraryToTag(current, libraryId, tagName)
        return current.map((tag) => ({
          ...tag,
          library_ids: tag.library_ids.filter((id) => id !== libraryId),
        }))
      })
    },
    [mutate],
  )

  return {
    tags,
    loading,
    saving,
    loadError,
    selectedTagId,
    setSelectedTagId,
    createTag,
    renameTag,
    removeTag,
    setTagLibraries,
    assignLibrary,
  }
}

/** 判断错误是否说明后端没有这个接口（旧版本 / 反向代理未更新）。 */
function isMissingEndpoint(err: unknown): boolean {
  const status = (err as { response?: { status?: number } })?.response?.status
  return status === 404 || status === 405 || status === 501
}
