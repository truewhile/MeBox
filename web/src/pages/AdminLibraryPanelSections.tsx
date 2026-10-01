import { FormEvent, useState } from 'react'
import { Folder, Loader2, Plus, Trash2 } from 'lucide-react'

import { LocalDirBrowserDialog } from '../components/LocalDirBrowserDialog'
import type { RootDraft } from './adminLibraryPanelModel'

type CreateFormProps = {
  name: string
  type: string
  coverURL: string
  roots: RootDraft[]
  createPerSubfolder: boolean
  creating: boolean
  onNameChange: (value: string) => void
  onTypeChange: (value: string) => void
  onCoverURLChange: (value: string) => void
  onCreatePerSubfolderChange: (value: boolean) => void
  onRootChange: (index: number, patch: Partial<RootDraft>) => void
  onAddRoot: () => void
  onRemoveRoot: (index: number) => void
  onSubmit: (e: FormEvent) => void
}

export function AdminLibraryCreateForm({
  name,
  type,
  coverURL,
  roots,
  createPerSubfolder,
  creating,
  onNameChange,
  onTypeChange,
  onCoverURLChange,
  onCreatePerSubfolderChange,
  onRootChange,
  onAddRoot,
  onRemoveRoot,
  onSubmit,
}: CreateFormProps) {
  const [browsingIndex, setBrowsingIndex] = useState<number | null>(null)

  const handleSelectDir = (selectedPath: string) => {
    if (browsingIndex === null) return
    const currentRoot = roots[browsingIndex]
    const segments = selectedPath.split(/[/\\]/).filter(Boolean)
    const baseName = segments.length > 0 ? segments[segments.length - 1] : ''
    const patch: Partial<RootDraft> = { path: selectedPath }
    if (!currentRoot?.name?.trim() && baseName) {
      patch.name = baseName
    }
    onRootChange(browsingIndex, patch)
    setBrowsingIndex(null)
  }

  return (
    <>
      <form onSubmit={onSubmit} className="glass-panel grid gap-3 md:grid-cols-4">
        <input
          required={!createPerSubfolder}
          className="input-base"
          placeholder={createPerSubfolder ? '父级媒体库名（批量模式忽略）' : '名称'}
          value={name}
          onChange={(e) => onNameChange(e.target.value)}
        />
        <select className="input-base" value={type} onChange={(e) => onTypeChange(e.target.value)}>
          <option value="movie">电影</option>
          <option value="tv">电视剧</option>
          <option value="variety">综艺</option>
          <option value="anime">动漫</option>
          <option value="music">音乐</option>
        </select>
        <input
          className="input-base md:col-span-2"
          placeholder="自定义封面 URL（可选）"
          value={coverURL}
          onChange={(e) => onCoverURLChange(e.target.value)}
        />
        <div className="md:col-span-4 space-y-2">
          {roots.map((root, index) => (
            <CreateRootRow
              key={index}
              root={root}
              index={index}
              canRemove={roots.length > 1}
              onChange={onRootChange}
              onBrowse={() => setBrowsingIndex(index)}
              onRemove={onRemoveRoot}
            />
          ))}
          {!createPerSubfolder && (
            <button type="button" className="inline-flex items-center gap-2 rounded-lg border px-3 py-2 text-sm" onClick={onAddRoot}>
              <Plus size={16} /> 添加路径
            </button>
          )}
        </div>
        <p className="md:col-span-4 -mt-2 text-xs text-sand-500">
          支持直接点选或手动输入；名称和类型与现有媒体库一致时，会自动把这里填写的路径追加到该媒体库。
        </p>
        <label className="md:col-span-4 flex items-center gap-2 text-sm text-ink-100">
          <input
            type="checkbox"
            className="h-4 w-4 accent-brand-400"
            checked={createPerSubfolder}
            onChange={(e) => onCreatePerSubfolderChange(e.target.checked)}
          />
          <span>按目录下每个子文件夹各建一个媒体库（媒体库名取子文件夹名）</span>
        </label>
        {createPerSubfolder && (
          <p className="md:col-span-4 -mt-2 text-xs text-sand-500">
            批处理模式：仅取上方第一个路径作为父级目录，会为其中每个子文件夹分别创建媒体库，可自选类型用于整体推断。
          </p>
        )}
        <button type="submit" className="neon-button md:col-span-4 disabled:opacity-50" disabled={creating}>
          {creating && <Loader2 size={16} className="animate-spin" />}
          {creating ? '创建中…' : createPerSubfolder ? '按目录批量创建' : '新建 / 追加路径'}
        </button>
      </form>

      {browsingIndex !== null && (
        <LocalDirBrowserDialog
          initialDir={roots[browsingIndex]?.path || undefined}
          title="选择媒体库目录"
          onSelect={handleSelectDir}
          onClose={() => setBrowsingIndex(null)}
        />
      )}
    </>
  )
}

type CreateRootRowProps = {
  root: RootDraft
  index: number
  canRemove: boolean
  onChange: (index: number, patch: Partial<RootDraft>) => void
  onBrowse: () => void
  onRemove: (index: number) => void
}

function CreateRootRow({ root, index, canRemove, onChange, onBrowse, onRemove }: CreateRootRowProps) {
  return (
    <div className="grid gap-2 rounded-xl border border-gray-200/80 bg-white/60 p-2 md:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto_auto]">
      <input
        className="input-base"
        placeholder="路径名称（如 国产剧）"
        value={root.name ?? ''}
        onChange={(e) => onChange(index, { name: e.target.value })}
      />
      <input
        required={index === 0}
        className="input-base"
        placeholder="本地或容器目录，如 /media/电视剧/国产剧"
        value={root.path}
        onChange={(e) => onChange(index, { path: e.target.value })}
      />
      <button
        type="button"
        onClick={onBrowse}
        className="inline-flex items-center justify-center gap-1 rounded-lg border border-gray-200 bg-white px-3 py-2 text-xs font-semibold text-ink-100 transition hover:bg-gray-50"
        title="点选浏览本地目录"
      >
        <Folder size={14} className="text-brand-400" />
        浏览
      </button>
      <button
        type="button"
        className="rounded-lg border border-red-400/40 px-3 text-red-400 hover:bg-red-400/10 disabled:opacity-40"
        disabled={!canRemove}
        onClick={() => onRemove(index)}
        title="删除路径"
      >
        <Trash2 size={16} />
      </button>
    </div>
  )
}

