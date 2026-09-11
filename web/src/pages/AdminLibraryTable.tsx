import { useEffect, useRef, useState, type DragEvent, type MouseEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Folder, GripVertical, Image, MoreVertical, Plus, Power, PowerOff, RefreshCw, Save, Trash2 } from 'lucide-react'

import { imageURL } from '../api/client'
import { LocalDirBrowserDialog } from '../components/LocalDirBrowserDialog'
import type { Library, LibraryRoot } from '../types'
import type { RootDraft } from './adminLibraryPanelModel'
import { displayLibraryRootName, displayLibraryRootPath, fallbackLibraryRoot } from './adminLibraryPanelModel'

type LibraryTableProps = {
  libs: Library[]
  editableRootDraft: (libraryID: string, root: LibraryRoot) => RootDraft
  onEditableRootChange: (libraryID: string, root: LibraryRoot, patch: Partial<RootDraft>) => void
  onSaveRoot: (libraryID: string, root: LibraryRoot) => void
  onScanRoot: (libraryID: string, root: LibraryRoot) => void
  onToggleRoot: (libraryID: string, root: LibraryRoot) => void
  onRemoveRoot: (library: Library, root: LibraryRoot) => void
  onScanLibrary: (library: Library) => void
  onRemoveLibrary: (library: Library) => void
  onAddLibraryRoot: (library: Library, path?: string, name?: string) => void
  onEditLibraryCover: (library: Library) => void
  onToggleCarousel: (library: Library) => void
  onReorder: (orderedLibs: Library[]) => void
}

export function AdminLibraryTable({ libs, ...actions }: LibraryTableProps) {
  const [browsingRoot, setBrowsingRoot] = useState<{ libraryID: string; root: LibraryRoot; initialPath?: string } | null>(null)
  const [addingRootLib, setAddingRootLib] = useState<Library | null>(null)
  const [draggingId, setDraggingId] = useState<string | null>(null)
  const [dragOverId, setDragOverId] = useState<string | null>(null)

  const handleSelectRootPath = (selectedPath: string) => {
    if (browsingRoot) {
      actions.onEditableRootChange(browsingRoot.libraryID, browsingRoot.root, { path: selectedPath })
      setBrowsingRoot(null)
    }
  }

  const handleSelectAddRoot = (selectedPath: string) => {
    if (addingRootLib) {
      const segments = selectedPath.split(/[/\\]/).filter(Boolean)
      const baseName = segments.length > 0 ? segments[segments.length - 1] : ''
      actions.onAddLibraryRoot(addingRootLib, selectedPath, baseName)
      setAddingRootLib(null)
    }
  }

  const handleReorder = (fromId: string, overId: string) => {
    if (fromId === overId) return
    const copy = [...libs]
    const fromIndex = copy.findIndex((l) => l.id === fromId)
    const overIndex = copy.findIndex((l) => l.id === overId)
    if (fromIndex < 0 || overIndex < 0) return
    const [moved] = copy.splice(fromIndex, 1)
    copy.splice(overIndex, 0, moved)
    actions.onReorder(copy)
  }

  const handleDrop = (e: DragEvent, overId: string) => {
    e.preventDefault()
    setDragOverId(null)
    setDraggingId(null)
    if (draggingId && overId !== draggingId) {
      handleReorder(draggingId, overId)
    }
  }

  return (
    <>
      <div className="glass-panel overflow-x-auto !p-3">
        <table className="w-full min-w-[960px] text-left text-sm">
          <thead className="text-xs uppercase tracking-wider text-sand-500">
            <tr>
              <th className="w-8 text-center" title="拖动排序">
                <GripVertical size={13} className="mx-auto text-gray-300" />
              </th>
              <th className="w-28 py-2">名称</th>
              <th>路径</th>
              <th className="w-20">类型</th>
              <th className="w-24">轮播</th>
              <th className="w-12 text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            {libs.map((library) => (
              <LibraryTableRow
                key={library.id}
                library={library}
                onBrowseRoot={(root) => setBrowsingRoot({ libraryID: library.id, root, initialPath: root.path })}
                onOpenAddRoot={() => setAddingRootLib(library)}
                dragging={draggingId === library.id}
                dragOver={dragOverId === library.id}
                onDragStart={(e) => {
                  e.dataTransfer.effectAllowed = 'move'
                  setDragOverId(null)
                  setDraggingId(library.id)
                }}
                onDragOver={(e) => {
                  e.preventDefault()
                  e.dataTransfer.dropEffect = 'move'
                  if (dragOverId !== library.id) setDragOverId(library.id)
                }}
                onDragEnd={() => {
                  setDragOverId(null)
                  setDraggingId(null)
                }}
                onDrop={(e) => handleDrop(e, library.id)}
                {...actions}
              />
            ))}
          </tbody>
        </table>
      </div>

      {browsingRoot && (
        <LocalDirBrowserDialog
          initialDir={browsingRoot.initialPath || undefined}
          title="选择媒体库路径"
          onSelect={handleSelectRootPath}
          onClose={() => setBrowsingRoot(null)}
        />
      )}

      {addingRootLib && (
        <LocalDirBrowserDialog
          title={`为「${addingRootLib.name}」选择来源目录`}
          onSelect={handleSelectAddRoot}
          onClose={() => setAddingRootLib(null)}
        />
      )}
    </>
  )
}

type LibraryTableRowProps = Omit<LibraryTableProps, 'libs'> & {
  library: Library
  onBrowseRoot: (root: LibraryRoot) => void
  onOpenAddRoot: () => void
  dragging?: boolean
  dragOver?: boolean
  onDragStart?: (e: DragEvent) => void
  onDragOver?: (e: DragEvent) => void
  onDragEnd?: () => void
  onDrop?: (e: DragEvent) => void
}

function LibraryTableRow({ library, dragging, dragOver, onDragStart, onDragOver, onDragEnd, onDrop, ...actions }: LibraryTableRowProps) {
  return (
    <tr
      draggable={false}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragEnd={onDragEnd}
      onDrop={onDrop}
      className={`border-t border-gray-200 transition-colors ${
        dragging ? 'bg-primary-400/10 opacity-60' : dragOver ? 'bg-primary-400/5' : ''
      }`}
    >
      <td className="py-2 text-center">
        <button
          type="button"
          draggable
          onDragStart={onDragStart}
          className="inline-flex cursor-grab items-center justify-center rounded p-1 text-gray-400 transition hover:bg-gray-100 hover:text-brand-500 active:cursor-grabbing"
          title="拖动以调整媒体库显示顺序"
        >
          <GripVertical size={16} />
        </button>
      </td>
      <td className="py-2 pr-3 font-medium text-ink-600">
        <div className="flex items-center gap-2">
          {library.cover_url && (
            <img
              src={imageURL(library.cover_url, library.updated_at)}
              alt=""
              loading="lazy"
              decoding="async"
              referrerPolicy="no-referrer"
              className="h-10 w-8 rounded object-cover"
              onError={(e) => {
                e.currentTarget.style.visibility = 'hidden'
              }}
            />
          )}
          <span>{library.name}</span>
        </div>
      </td>
      <td className="py-1.5 text-ink-100">
        <LibraryRootsCell library={library} {...actions} />
      </td>
      <td className="px-3 text-ink-100">{library.type}</td>
      <td className="py-2 text-ink-100">
        <CarouselToggle library={library} onToggleCarousel={actions.onToggleCarousel} />
      </td>
      <td className="py-2 text-right">
        <LibraryActionsCell library={library} {...actions} />
      </td>
    </tr>
  )
}

function CarouselToggle({ library, onToggleCarousel }: { library: Library; onToggleCarousel: (library: Library) => void }) {
  const on = Boolean(library.carousel_enabled)
  return (
    <button
      type="button"
      onClick={() => onToggleCarousel(library)}
      className={`inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs font-semibold transition ${
        on
          ? 'border-brand-500/50 bg-brand-500/10 text-brand-500'
          : 'border-gray-300 bg-white text-ink-50 hover:border-gray-400'
      }`}
      title={on ? '已开启首页海报轮播（点击关闭）' : '未开启首页海报轮播（点击开启）'}
    >
      <span className={`h-3.5 w-3.5 rounded-full ${on ? 'bg-brand-500' : 'bg-gray-300'}`} />
      {on ? '参与轮播' : '未参与'}
    </button>
  )
}

function LibraryRootsCell({ library, ...actions }: LibraryTableRowProps) {
  const roots = library.roots?.length ? library.roots : [fallbackLibraryRoot(library)]
  return (
    <div className="min-w-[520px] space-y-1">
      {roots.map((root) => (
        <ExistingRootEditor key={root.id || root.path} library={library} root={root} {...actions} />
      ))}
    </div>
  )
}

type RootEditorProps = Omit<LibraryTableRowProps, 'library'> & {
  library: Library
  root: LibraryRoot
}

function ExistingRootEditor({ library, root, ...actions }: RootEditorProps) {
  const draft = actions.editableRootDraft(library.id, root)
  return (
    <div className="grid items-center gap-1.5 rounded-lg border border-gray-200/80 bg-gray-50/60 p-1.5 xl:grid-cols-[minmax(92px,0.65fr)_minmax(240px,2fr)_auto_auto]">
      {root.id ? <EditableRootFields library={library} root={root} draft={draft} {...actions} /> : <ReadonlyRootFields root={root} />}
      <RootStatus enabled={draft.enabled ?? root.enabled} />
      <RootActionButtons library={library} root={root} draft={draft} {...actions} />
    </div>
  )
}

function ReadonlyRootFields({ root }: { root: LibraryRoot }) {
  return (
    <>
      <span className="truncate rounded-md bg-white/80 px-2.5 py-1.5 text-xs text-ink-600">{displayLibraryRootName(root.name, root.path)}</span>
      <span className="min-w-0 truncate rounded-md bg-white/80 px-2.5 py-1.5 text-xs text-ink-100" title={displayLibraryRootPath(root.path)}>
        {displayLibraryRootPath(root.path)}
      </span>
    </>
  )
}

function EditableRootFields({ library, root, draft, onEditableRootChange, onBrowseRoot }: RootEditorProps & { draft: RootDraft }) {
  return (
    <>
      <input
        className="h-9 w-full rounded-lg border border-gray-200 bg-white/80 px-3 text-xs text-gray-900 outline-none transition focus:border-brand-500 focus:ring-2 focus:ring-brand-100/60"
        placeholder="路径名称"
        value={draft.name ?? ''}
        onChange={(e) => onEditableRootChange(library.id, root, { name: e.target.value })}
      />
      <div className="flex items-center gap-1">
        <input
          className="h-9 flex-1 min-w-0 rounded-lg border border-gray-200 bg-white/80 px-3 text-xs text-gray-900 outline-none transition focus:border-brand-500 focus:ring-2 focus:ring-brand-100/60"
          placeholder="真实路径"
          value={draft.path}
          onChange={(e) => onEditableRootChange(library.id, root, { path: e.target.value })}
        />
        <button
          type="button"
          onClick={() => onBrowseRoot(root)}
          className="inline-flex h-9 items-center gap-1 rounded-lg border border-gray-200 bg-white px-2.5 text-xs font-semibold text-ink-100 transition hover:bg-gray-50"
          title="点选浏览本地目录"
        >
          <Folder size={13} className="text-brand-400" />
          浏览
        </button>
      </div>
    </>
  )
}

function RootStatus({ enabled }: { enabled: boolean }) {
  return (
    <span
      className={`whitespace-nowrap rounded-md border px-2 py-1 text-xs ${
        enabled ? 'border-emerald-300/60 text-emerald-600' : 'border-gray-300 text-ink-50'
      }`}
    >
      {enabled ? '启用' : '禁用'}
    </span>
  )
}

function RootActionButtons({ library, root, draft, ...actions }: RootEditorProps & { draft: RootDraft }) {
  const enabled = draft.enabled ?? root.enabled
  return (
    <ActionMenu label="路径操作">
      {root.id && (
        <MenuButton
          icon={<Save size={14} />}
          label="保存"
          onClick={() => actions.onSaveRoot(library.id, root)}
        >
          保存
        </MenuButton>
      )}
      <MenuButton
        icon={<RefreshCw size={14} />}
        label="扫描"
        onClick={() => actions.onScanRoot(library.id, root)}
      >
        扫描
      </MenuButton>
      {root.id && (
        <MenuButton
          icon={enabled ? <PowerOff size={14} /> : <Power size={14} />}
          label={enabled ? '禁用' : '启用'}
          onClick={() => actions.onToggleRoot(library.id, root)}
        >
          {enabled ? '禁用' : '启用'}
        </MenuButton>
      )}
      {root.id && (
        <MenuButton
          danger
          icon={<Trash2 size={14} />}
          label="删除"
          onClick={() => actions.onRemoveRoot(library, root)}
        >
          删除
        </MenuButton>
      )}
    </ActionMenu>
  )
}

function LibraryActionsCell({ library, onScanLibrary, onRemoveLibrary, onOpenAddRoot, onEditLibraryCover }: LibraryTableRowProps) {
  return (
    <ActionMenu label="媒体库操作">
      <MenuButton icon={<RefreshCw size={14} />} label="扫描" onClick={() => onScanLibrary(library)}>
        扫描
      </MenuButton>
      <MenuButton icon={<Plus size={14} />} label="添加来源" onClick={onOpenAddRoot}>
        添加来源
      </MenuButton>
      <MenuButton icon={<Image size={14} />} label="自定义封面" onClick={() => onEditLibraryCover(library)}>
        自定义封面
      </MenuButton>
      <MenuButton danger icon={<Trash2 size={14} />} label="删除" onClick={() => onRemoveLibrary(library)}>
        删除
      </MenuButton>
    </ActionMenu>
  )
}

function ActionMenu({ label, children }: { label: string; children: ReactNode }) {
  const [isOpen, setIsOpen] = useState(false)
  const [coords, setCoords] = useState<{ top?: number; bottom?: number; right: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  const toggleMenu = (e: MouseEvent) => {
    e.stopPropagation()
    if (isOpen) {
      setIsOpen(false)
      return
    }
    if (triggerRef.current) {
      const rect = triggerRef.current.getBoundingClientRect()
      const spaceBelow = window.innerHeight - rect.bottom
      const estimatedHeight = 180
      const openUpward = spaceBelow < estimatedHeight && rect.top > estimatedHeight
      setCoords({
        top: openUpward ? undefined : rect.bottom + 4,
        bottom: openUpward ? window.innerHeight - rect.top + 4 : undefined,
        right: window.innerWidth - rect.right,
      })
      setIsOpen(true)
    }
  }

  useEffect(() => {
    if (!isOpen) return
    const handleClickOutside = (e: globalThis.MouseEvent) => {
      if (
        menuRef.current &&
        !menuRef.current.contains(e.target as Node) &&
        triggerRef.current &&
        !triggerRef.current.contains(e.target as Node)
      ) {
        setIsOpen(false)
      }
    }
    const handleScrollOrResize = () => setIsOpen(false)
    window.addEventListener('mousedown', handleClickOutside)
    window.addEventListener('scroll', handleScrollOrResize, true)
    window.addEventListener('resize', handleScrollOrResize)
    return () => {
      window.removeEventListener('mousedown', handleClickOutside)
      window.removeEventListener('scroll', handleScrollOrResize, true)
      window.removeEventListener('resize', handleScrollOrResize)
    }
  }, [isOpen])

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        onClick={toggleMenu}
        className={`inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg border transition ${
          isOpen
            ? 'border-brand-500 bg-brand-500/10 text-brand-500'
            : 'border-gray-200 bg-white text-ink-50 hover:border-primary-400/50 hover:text-brand-500'
        }`}
        title={label}
      >
        <MoreVertical size={16} />
      </button>

      {isOpen &&
        coords &&
        createPortal(
          <div
            ref={menuRef}
            style={{
              position: 'fixed',
              top: coords.top !== undefined ? `${coords.top}px` : undefined,
              bottom: coords.bottom !== undefined ? `${coords.bottom}px` : undefined,
              right: `${coords.right}px`,
              zIndex: 99999,
            }}
            className="min-w-32 rounded-xl border border-gray-200/90 bg-white p-1.5 shadow-2xl backdrop-blur"
            onClick={() => setIsOpen(false)}
          >
            {children}
          </div>,
          document.body,
        )}
    </>
  )
}

function MenuButton({
  icon,
  label,
  danger,
  onClick,
  children,
}: {
  icon: ReactNode
  label: string
  danger?: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      className={`flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-xs font-medium transition ${
        danger
          ? 'text-red-500 hover:bg-red-50'
          : 'text-ink-100 hover:bg-gray-100 hover:text-brand-500'
      }`}
      title={label}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
    >
      {icon}
      <span>{children}</span>
    </button>
  )
}
