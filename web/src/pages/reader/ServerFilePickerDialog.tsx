import { useCallback, useEffect, useState } from 'react'
import { ChevronRight, FileAudio, FileText, Folder, FolderPlus, HardDrive, Loader2, X } from 'lucide-react'
import toast from 'react-hot-toast'

import { filesAPI, type FileEntry } from '../../api/files'

function apiErrorMessage(err: unknown): string {
  if (typeof err === 'object' && err !== null && 'response' in err) {
    const res = (err as { response?: { data?: { error?: string; message?: string } } }).response
    if (res?.data?.error) return res.data.error
    if (res?.data?.message) return res.data.message
  }
  if (err instanceof Error) return err.message
  return '请求失败'
}

function formatSize(bytes: number): string {
  if (!bytes) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)}${units[unit]}`
}

/**
 * ServerFilePickerDialog 选择服务器上已有的文件或目录。
 *
 * 走 /api/files（仅管理员，且只能浏览已配置的存储根目录），供阅读模块
 * 「从服务器导入书籍 / 有声书」使用。
 */
export function ServerFilePickerDialog({
  mode,
  extensions,
  title,
  initialDir,
  onSelect,
  onClose,
}: {
  mode: 'file' | 'dir'
  /** mode=file 时限制可选的后缀，小写带点（如 ['.txt', '.epub']）；留空表示不限制 */
  extensions?: string[]
  title: string
  initialDir?: string
  onSelect: (path: string) => void
  onClose: () => void
}) {
  const [listing, setListing] = useState<{
    path: string
    parent?: string
    roots?: { label: string; path: string }[]
    entries: FileEntry[] | null
  } | null>(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async (target: string) => {
    setLoading(true)
    try {
      const data = await filesAPI.list(target)
      setListing({ path: data.path, parent: data.parent, roots: data.roots, entries: data.entries })
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load(initialDir ?? '')
  }, [initialDir, load])

  const atRoot = !listing?.path
  // 后端返回的 ext 不带点（"epub"），这里统一去掉点再比较
  const normalizeExt = (raw?: string) => (raw ?? '').toLowerCase().replace(/^\./, '')
  const wanted = (extensions ?? []).map(normalizeExt)
  const accept = (entry: FileEntry) => {
    if (entry.is_dir) return true
    if (mode === 'dir') return false
    if (wanted.length === 0) return true
    return wanted.includes(normalizeExt(entry.ext))
  }

  const roots = listing?.roots ?? []
  const entries = (listing?.entries ?? []).filter(accept)
  // 根目录列表和目录内条目是两个来源，空态判断要分别看
  const itemCount = atRoot ? roots.length : entries.length
  const canPickCurrent = mode === 'dir' && !atRoot

  return (
    <div
      className="fixed inset-0 z-[110] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="flex max-h-[80vh] w-full max-w-2xl flex-col overflow-hidden rounded-3xl border border-white/70 bg-white shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
          <h3 className="font-display text-lg font-bold text-ink-600">{title}</h3>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl p-1.5 text-ink-50 transition hover:bg-gray-100 hover:text-ink-600"
            title="关闭"
          >
            <X size={20} />
          </button>
        </div>

        <div className="flex items-center gap-2 border-b border-gray-100 px-6 py-2.5 text-xs text-sand-500">
          {atRoot ? (
            <span className="text-ink-50">选择存储位置</span>
          ) : (
            <>
              <button type="button" className="hover:text-brand-500" onClick={() => load('')}>
                存储位置
              </button>
              <ChevronRight size={12} />
              <span className="truncate text-ink-50" title={listing?.path}>
                {listing?.path}
              </span>
            </>
          )}
        </div>

        <div className="min-h-[280px] flex-1 overflow-y-auto p-3">
          {loading ? (
            <div className="flex justify-center py-10 text-ink-50">
              <Loader2 className="animate-spin" />
            </div>
          ) : itemCount === 0 ? (
            <p className="py-10 text-center text-sm text-sand-500">
              {atRoot
                ? '没有可用的存储位置'
                : mode === 'dir'
                  ? '该目录下没有子目录，可直接选择当前目录'
                  : '该目录下没有可选文件'}
            </p>
          ) : (
            <div className="space-y-1">
              {atRoot ? (
                roots.map((root) => (
                  <button
                    key={root.path}
                    type="button"
                    className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left text-sm transition hover:bg-gray-50"
                    onClick={() => load(root.path)}
                  >
                    <HardDrive size={16} className="text-brand-400" />
                    <span className="flex-1 truncate text-ink-600">{root.label}</span>
                    <span className="truncate text-xs text-sand-400">{root.path}</span>
                  </button>
                ))
              ) : (
                <>
                  {(listing?.parent ?? '') !== '' && (
                    <button
                      type="button"
                      className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left text-sm text-ink-50 transition hover:bg-gray-50"
                      onClick={() => load(listing?.parent ?? '')}
                    >
                      <FolderPlus size={16} className="text-sand-400" />
                      <span>..（上级目录）</span>
                    </button>
                  )}
                  {entries.map((entry) => (
                    <button
                      key={entry.path}
                      type="button"
                      className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left text-sm transition hover:bg-gray-50"
                      onClick={() => (entry.is_dir ? load(entry.path) : onSelect(entry.path))}
                    >
                      {entry.is_dir ? (
                        <Folder size={16} className="text-brand-400" />
                      ) : normalizeExt(entry.ext) === 'strm' ? (
                        <FileAudio size={16} className="text-sand-400" />
                      ) : (
                        <FileText size={16} className="text-sand-400" />
                      )}
                      <span className="flex-1 truncate text-ink-600">{entry.name}</span>
                      {!entry.is_dir && <span className="text-xs text-sand-400">{formatSize(entry.size)}</span>}
                    </button>
                  ))}
                </>
              )}
            </div>
          )}
        </div>

        <div className="flex items-center justify-between border-t border-gray-100 px-6 py-3">
          <span className="text-xs text-sand-500">
            {atRoot
              ? '先选择一个存储位置'
              : mode === 'dir'
                ? '单击目录进入下一级，点击「选择当前目录」完成选择'
                : '单击目录进入下一级，单击文件即完成选择'}
          </span>
          {canPickCurrent && (
            <button type="button" className="neon-button" disabled={loading} onClick={() => onSelect(listing?.path ?? '')}>
              选择当前目录
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
