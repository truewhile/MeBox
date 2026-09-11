import { FormEvent, useEffect, useState } from 'react'
import toast from 'react-hot-toast'

import { libraryAPI } from '../api/library'
import type { Library, LibraryRoot } from '../types'
import { invalidateLibraries } from '../utils/libraryCache'
import { confirmAction } from '../components/confirmAction'
import { apiErrorMessage, createRootPayload, displayLibraryRootName, displayLibraryRootPath, emptyRootDraft, rootDraftKey, type RootDraft } from './adminLibraryPanelModel'

export function useAdminLibraryPanel() {
  const { libs, refresh } = useAdminLibraryList()
  const createForm = useCreateLibraryForm(refresh)
  const editableRoots = useEditableRootDrafts()
  const rootActions = useEditableLibraryRootActions(refresh, editableRoots)
  const libraryActions = useLibraryActions(refresh)

  return { libs, createForm, editableRoots, rootActions, libraryActions }
}

function useAdminLibraryList() {
  const [libs, setLibs] = useState<Library[]>([])
  const refresh = () => {
    // 后台任何库变更都会走到这里；顺带清掉前台会话缓存，
    // 避免返回首页/媒体库页后 30 秒 TTL 内还显示旧列表。
    invalidateLibraries()
    return libraryAPI
      .list({ includeHidden: true })
      .then((libs) => setLibs(libs.filter((l) => !l.is_remote_emby)))  // 远程挂载库只读，不在后台管理列表内
  }

  useEffect(() => {
    refresh().catch(() => undefined)
  }, [])

  return { libs, refresh }
}

function useCreateLibraryForm(refresh: () => Promise<void>) {
  const [name, setName] = useState('')
  const [roots, setRoots] = useState<RootDraft[]>([emptyRootDraft()])
  const [type, setType] = useState('movie')
  const [coverURL, setCoverURL] = useState('')
  const [createPerSubfolder, setCreatePerSubfolder] = useState(false)
  // 提交中标记：防止重复点击创建出多个媒体库
  const [creating, setCreating] = useState(false)

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault()
    if (creating) return
    setCreating(true)
    try {
      if (createPerSubfolder) {
        const parentPath = roots[0]?.path?.trim()
        if (!parentPath) {
          toast.error('请先选择或填写父级目录')
          return
        }
        const { libraries } = await libraryAPI.createPerSubfolder(parentPath, type, coverURL.trim())
        toast.success(`已按目录创建 ${libraries.length} 个媒体库`)
      } else {
        const payload = createRootPayload(roots)
        if (payload.length === 0) {
          toast.error('请至少填写一个路径')
          return
        }
        await libraryAPI.createWithRoots(name, type, payload, coverURL.trim())
        toast.success('媒体库已保存')
      }
      setName('')
      setRoots([emptyRootDraft()])
      setCoverURL('')
      setCreatePerSubfolder(false)
    } catch (err: unknown) {
      toast.error(apiErrorMessage(err, '创建失败'))
    } finally {
      setCreating(false)
    }
    await refresh().catch(() => undefined)
  }

  const updateRoot = (index: number, patch: Partial<RootDraft>) => {
    setRoots((prev) => prev.map((root, i) => (i === index ? { ...root, ...patch } : root)))
  }

  return {
    name,
    type,
    coverURL,
    roots,
    createPerSubfolder,
    creating,
    setName,
    setType,
    setCoverURL,
    setCreatePerSubfolder,
    updateRoot,
    addRoot: () => setRoots((prev) => [...prev, emptyRootDraft()]),
    removeRoot: (index: number) => setRoots((prev) => (prev.length <= 1 ? prev : prev.filter((_, i) => i !== index))),
    handleCreate,
  }
}

function useEditableRootDrafts() {
  const [rootDrafts, setRootDrafts] = useState<Record<string, RootDraft>>({})

  const editableRootDraft = (libraryID: string, root: LibraryRoot): RootDraft => {
    const key = rootDraftKey(libraryID, root.id)
    return rootDrafts[key] ?? {
      name: displayLibraryRootName(root.name, root.path),
      path: displayLibraryRootPath(root.path),
      enabled: root.enabled,
      sort_order: root.sort_order,
    }
  }

  const setEditableRootDraft = (libraryID: string, root: LibraryRoot, patch: Partial<RootDraft>) => {
    const key = rootDraftKey(libraryID, root.id)
    setRootDrafts((prev) => ({ ...prev, [key]: { ...editableRootDraft(libraryID, root), ...patch } }))
  }

  const clearEditableRootDraft = (libraryID: string, rootID: string) => {
    setRootDrafts((prev) => {
      const next = { ...prev }
      delete next[rootDraftKey(libraryID, rootID)]
      return next
    })
  }

  return { editableRootDraft, setEditableRootDraft, clearEditableRootDraft }
}

type EditableRootDrafts = ReturnType<typeof useEditableRootDrafts>

function useEditableLibraryRootActions(refresh: () => Promise<void>, drafts: EditableRootDrafts) {
  const saveLibraryRoot = async (libraryID: string, root: LibraryRoot) => {
    const draft = drafts.editableRootDraft(libraryID, root)
    if (!draft.path?.trim()) {
      toast.error('请填写路径')
      return
    }
    await libraryAPI.updateRoot(libraryID, root.id, {
      name: draft.name?.trim(),
      path: draft.path.trim(),
      enabled: draft.enabled,
      sort_order: draft.sort_order,
    })
    drafts.clearEditableRootDraft(libraryID, root.id)
    toast.success('路径已保存')
    await refresh()
  }

  const scanLibraryRoot = async (libraryID: string, root: LibraryRoot) => {
    if (!root.id) return
    await libraryAPI.scanRoot(libraryID, root.id)
    toast.success('路径扫描已加入后台任务')
  }

  const toggleLibraryRoot = async (libraryID: string, root: LibraryRoot) => {
    const enabled = !drafts.editableRootDraft(libraryID, root).enabled
    drafts.setEditableRootDraft(libraryID, root, { enabled })
    await libraryAPI.updateRoot(libraryID, root.id, { enabled })
    await refresh()
  }

  const removeLibraryRoot = async (library: Library, root: LibraryRoot) => {
    if (!(await confirmAction({ title: '删除媒体库路径', message: `确定删除「${displayLibraryRootPath(root.path)}」?`, confirmText: '删除' }))) return
    await libraryAPI.removeRoot(library.id, root.id)
    toast.success('路径已删除')
    await refresh()
  }

  return { saveLibraryRoot, scanLibraryRoot, toggleLibraryRoot, removeLibraryRoot }
}

function useLibraryActions(refresh: () => Promise<void>) {
  const scanLibrary = async (library: Library) => {
    const result = await libraryAPI.scan(library.id)
    if (result.queued) toast.success(result.message || '媒体库扫描已加入后台队列，会自动入库')
    else toast.success(`扫描完成，新增 ${result.added ?? 0}，更新 ${result.updated ?? 0}`)
  }

  const toggleCarouselLibrary = async (library: Library) => {
    const next = !library.carousel_enabled
    await libraryAPI.update(library.id, { carousel_enabled: next })
    toast.success(next ? `「${library.name}」已加入首页轮播` : `「${library.name}」已移出首页轮播`)
    await refresh()
  }

  const reorderLibraries = async (orderedLibs: Library[]) => {
    await libraryAPI.reorder(orderedLibs.map((l) => l.id))
    await refresh()
  }

  const removeLibrary = async (library: Library) => {
    if (!(await confirmAction({ title: '删除媒体库', message: `确定删除「${library.name}」?`, confirmText: '删除' }))) return
    await libraryAPI.remove(library.id)
    toast.success('已删除')
    await refresh()
  }

  const addLibraryRoot = async (library: Library, path?: string, name?: string) => {
    if (!path) {
      const p = window.prompt(`为「${library.name}」添加来源目录：`)
      if (!p?.trim()) return
      path = p.trim()
      name = (window.prompt('来源名称（可选）：') ?? '').trim()
    }
    await libraryAPI.addRoot(library.id, { path: path.trim(), name: (name ?? '').trim(), enabled: true })
    toast.success('来源目录已添加')
    await refresh()
  }

  const editLibraryCover = async (library: Library) => {
    const coverURL = window.prompt('自定义封面 URL（留空可清除）：', library.cover_url ?? '')
    if (coverURL === null) return
    await libraryAPI.update(library.id, { cover_url: coverURL.trim() })
    toast.success(coverURL.trim() ? '媒体库封面已保存' : '媒体库封面已清除')
    await refresh()
  }

  return { scanLibrary, removeLibrary, addLibraryRoot, editLibraryCover, toggleCarouselLibrary, reorderLibraries }
}
