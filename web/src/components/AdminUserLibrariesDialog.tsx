import { useEffect, useState } from 'react'
import toast from 'react-hot-toast'
import {
  Check,
  CheckSquare,
  Film,
  FolderLock,
  Layers,
  Loader2,
  Square,
  Tv,
  X,
} from 'lucide-react'

import { adminAPI } from '../api/admin'
import { libraryAPI } from '../api/library'
import type { Library, User } from '../types'
import { libraryDisplayPath } from '../pages/libraryDisplayModel'

type AdminUserLibrariesDialogProps = {
  user: User | null
  isOpen: boolean
  onClose: () => void
  onSaved: (updatedUser: User) => void
}

export function AdminUserLibrariesDialog({
  user,
  isOpen,
  onClose,
  onSaved,
}: AdminUserLibrariesDialogProps) {
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [libraries, setLibraries] = useState<Library[]>([])
  const [mode, setMode] = useState<'all' | 'custom'>('all')
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(new Set())

  useEffect(() => {
    if (!isOpen || !user) return

    setLoading(true)
    libraryAPI
      .list({ includeHidden: true })
      .then((libs) => {
        setLibraries(libs ?? [])
      })
      .catch(() => {
        toast.error('加载媒体库列表失败')
      })
      .finally(() => {
        setLoading(false)
      })

    const initialIDs = user.allowed_library_ids ?? []
    if (initialIDs.length > 0) {
      setMode('custom')
      setSelectedIDs(new Set(initialIDs))
    } else {
      setMode('all')
      setSelectedIDs(new Set())
    }
  }, [isOpen, user])

  if (!isOpen || !user) return null

  const toggleLibrary = (libID: string) => {
    setSelectedIDs((prev) => {
      const next = new Set(prev)
      if (next.has(libID)) {
        next.delete(libID)
      } else {
        next.add(libID)
      }
      return next
    })
  }

  const handleSelectAll = () => {
    setSelectedIDs(new Set(libraries.map((l) => l.id)))
  }

  const handleSelectNone = () => {
    setSelectedIDs(new Set())
  }

  const handleSave = async () => {
    setSaving(true)
    try {
      let payloadIDs: string[] | null = null
      if (mode === 'custom') {
        const arr = Array.from(selectedIDs)
        // 如果勾选了全部，或者勾选为空
        if (arr.length === 0) {
          toast.error('请至少选择一个媒体库，或切换为“允许访问全部媒体库”')
          setSaving(false)
          return
        }
        if (arr.length < libraries.length) {
          payloadIDs = arr
        }
      }

      const updated = await adminAPI.updateUserLibraries(user.id, payloadIDs)
      toast.success(
        payloadIDs && payloadIDs.length > 0
          ? `已成功配置用户【${user.username}】仅可访问 ${payloadIDs.length} 个媒体库`
          : `已恢复用户【${user.username}】全库访问权限`,
      )
      onSaved(updated)
      onClose()
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '保存配置失败'
      toast.error(msg)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4 backdrop-blur-sm animate-in fade-in-0"
      onClick={onClose}
    >
      <div
        className="w-full max-w-xl overflow-hidden rounded-3xl border border-white/70 bg-white shadow-2xl transition-all"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between border-b border-sand-100 p-5">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-2xl bg-brand-50 text-brand-600">
              <FolderLock size={20} />
            </div>
            <div>
              <h3 className="font-display text-lg font-bold text-ink-600">
                配置媒体库访问权限
              </h3>
              <p className="text-xs text-sand-500">
                用户：<span className="font-semibold text-ink-600">{user.username}</span>
                {user.role === 'admin' && (
                  <span className="ml-2 rounded-full bg-amber-50 px-2 py-0.5 text-[10px] font-semibold text-amber-600 border border-amber-200">
                    管理员拥有所有库访问权限
                  </span>
                )}
              </p>
            </div>
          </div>
          <button
            onClick={onClose}
            className="rounded-xl p-2 text-sand-500 hover:bg-sand-100 hover:text-ink-600 transition-colors"
          >
            <X size={18} />
          </button>
        </div>

        {/* Content */}
        <div className="p-5 space-y-4 max-h-[65vh] overflow-y-auto">
          {/* 模式选择 */}
          <div className="grid grid-cols-2 gap-3">
            <button
              type="button"
              onClick={() => setMode('all')}
              className={`flex items-center gap-3 rounded-2xl border p-3.5 text-left transition-all ${
                mode === 'all'
                  ? 'border-brand-400 bg-brand-50/70 text-brand-800 shadow-sm ring-1 ring-brand-400'
                  : 'border-sand-200 bg-white text-ink-600 hover:border-sand-300 hover:bg-sand-50/50'
              }`}
            >
              <div
                className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-xl ${
                  mode === 'all' ? 'bg-brand-500 text-white' : 'bg-sand-100 text-sand-500'
                }`}
              >
                <Layers size={16} />
              </div>
              <div className="min-w-0 flex-1">
                <div className="text-xs font-bold">全部媒体库 (默认)</div>
                <div className="text-[11px] text-sand-500">用户可访问系统所有已建媒体库</div>
              </div>
            </button>

            <button
              type="button"
              onClick={() => {
                setMode('custom')
                if (selectedIDs.size === 0) {
                  setSelectedIDs(new Set(libraries.map((l) => l.id)))
                }
              }}
              className={`flex items-center gap-3 rounded-2xl border p-3.5 text-left transition-all ${
                mode === 'custom'
                  ? 'border-brand-400 bg-brand-50/70 text-brand-800 shadow-sm ring-1 ring-brand-400'
                  : 'border-sand-200 bg-white text-ink-600 hover:border-sand-300 hover:bg-sand-50/50'
              }`}
            >
              <div
                className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-xl ${
                  mode === 'custom' ? 'bg-brand-500 text-white' : 'bg-sand-100 text-sand-500'
                }`}
              >
                <FolderLock size={16} />
              </div>
              <div className="min-w-0 flex-1">
                <div className="text-xs font-bold">指定可访问媒体库</div>
                <div className="text-[11px] text-sand-500">仅允许访问管理员勾选的媒体库</div>
              </div>
            </button>
          </div>

          {/* 媒体库列表 */}
          {mode === 'custom' && (
            <div className="space-y-2 pt-2 border-t border-sand-100 animate-in fade-in-0 duration-200">
              <div className="flex items-center justify-between text-xs text-sand-500 px-1">
                <span>
                  已选择{' '}
                  <strong className="text-brand-600">{selectedIDs.size}</strong> /{' '}
                  {libraries.length} 个媒体库
                </span>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={handleSelectAll}
                    className="inline-flex items-center gap-1 text-[11px] font-semibold text-brand-600 hover:text-brand-700"
                  >
                    <CheckSquare size={13} />
                    全选
                  </button>
                  <span>·</span>
                  <button
                    type="button"
                    onClick={handleSelectNone}
                    className="inline-flex items-center gap-1 text-[11px] font-semibold text-sand-500 hover:text-sand-700"
                  >
                    <Square size={13} />
                    全不选
                  </button>
                </div>
              </div>

              {loading ? (
                <div className="flex items-center justify-center py-12 text-sand-500 text-xs gap-2">
                  <Loader2 size={16} className="animate-spin text-brand-500" />
                  正在加载媒体库…
                </div>
              ) : libraries.length === 0 ? (
                <div className="py-10 text-center text-xs text-sand-500 bg-sand-50/50 rounded-2xl">
                  暂无可用媒体库
                </div>
              ) : (
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  {libraries.map((lib) => {
                    const isChecked = selectedIDs.has(lib.id)
                    const isTv =
                      lib.type === 'tv' || lib.type === 'anime' || lib.type === 'variety'
                    return (
                      <div
                        key={lib.id}
                        onClick={() => toggleLibrary(lib.id)}
                        className={`group flex cursor-pointer items-center justify-between rounded-xl border p-3 transition-all ${
                          isChecked
                            ? 'border-brand-300 bg-brand-50/40 shadow-xs'
                            : 'border-sand-200 bg-white hover:border-sand-300 hover:bg-sand-50/40'
                        }`}
                      >
                        <div className="flex min-w-0 items-center gap-2.5">
                          <div
                            className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ${
                              isChecked
                                ? 'bg-brand-500 text-white'
                                : 'bg-sand-100 text-sand-500 group-hover:text-sand-700'
                            }`}
                          >
                            {isTv ? <Tv size={15} /> : <Film size={15} />}
                          </div>
                          <div className="min-w-0 flex-1">
                            <p className="truncate text-xs font-semibold text-ink-600">
                              {lib.name}
                              {lib.is_remote_emby ? (
                                <span className="ml-1.5 rounded bg-sky-50 px-1.5 py-0.5 text-[10px] font-bold text-sky-700">
                                  Emby 挂载
                                </span>
                              ) : null}
                            </p>
                            <p
                              className="truncate text-[10px] text-sand-500"
                              title={lib.is_remote_emby ? lib.remote_source || lib.name : lib.path}
                            >
                              {lib.is_remote_emby
                                ? `远程 · ${lib.remote_source || 'Emby'}`
                                : `${lib.type} · ${libraryDisplayPath(lib.path)}`}
                            </p>
                          </div>
                        </div>
                        <div
                          className={`flex h-5 w-5 shrink-0 items-center justify-center rounded-md border transition-colors ${
                            isChecked
                              ? 'border-brand-500 bg-brand-500 text-white'
                              : 'border-sand-300 bg-white group-hover:border-sand-400'
                          }`}
                        >
                          {isChecked && <Check size={12} className="stroke-[3]" />}
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-end gap-2.5 border-t border-sand-100 bg-sand-50/60 px-5 py-3.5">
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl border border-sand-200 bg-white px-4 py-2 text-xs font-semibold text-ink-600 hover:bg-sand-100 transition-colors"
          >
            取消
          </button>
          <button
            type="button"
            disabled={saving || loading}
            onClick={handleSave}
            className="neon-button inline-flex items-center gap-2 px-5 py-2 text-xs font-semibold disabled:opacity-50"
          >
            {saving && <Loader2 size={13} className="animate-spin" />}
            <span>保存配置</span>
          </button>
        </div>
      </div>
    </div>
  )
}
