import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import toast from 'react-hot-toast'
import {
  BookOpen,
  Check,
  ChevronDown,
  FileUp,
  FolderOpen,
  Headphones,
  LayoutGrid,
  List,
  Loader2,
  MoreHorizontal,
  RefreshCw,
  Rows3,
  Settings2,
  SlidersHorizontal,
} from 'lucide-react'

import { readerAPI, type ReaderBook } from '../../api/reader'
import { BookGroupBar } from '../../components/BookGroupBar'
import { BookGroupPickerDialog } from '../../components/BookGroupPickerDialog'
import { ManageBookGroupsDialogView } from '../../components/ManageBookGroupsDialogView'
import { confirmAction } from '../../components/confirmAction'
import { useBookGroups } from '../../hooks/useBookGroups'
import { useAuthStore } from '../../stores/auth'
import { useReaderSettingsStore } from '../../stores/readerSettings'
import { buildBookGroupTabs, filterBooksByGroup, groupIdOfBook, ALL_GROUP_ID } from '../../utils/readerBookGroups'
import { BookshelfCard } from './BookshelfCard'
import { BookshelfSettingsDialog } from './BookshelfSettingsDialog'
import { SHELF_SORT_OPTIONS, sortShelfBooks } from './bookshelfModel'
import { ReaderModeSwitch } from './ReaderModeSwitch'
import { ServerFilePickerDialog } from './ServerFilePickerDialog'

// 首页阅读模式的书架内容（首页切换与 /reader 路由共用）。
// 结构仿 legado 书架：顶部操作区 + 分组栏 + 网格/列表/紧凑列表三套布局 +
// 可配置排序与显示项。布局、排序、显示项存在 readerSettings store；
// 分组按用户存在服务端（见 utils/readerBookGroups.ts、hooks/useBookGroups.ts）。

// 网格列数 → Tailwind 栅格类。窄屏保留 2–3 列兜底，避免固定列数在手机上挤成一团。
const GRID_COLUMNS_CLASS: Record<number, string> = {
  0: 'grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-8',
  2: 'grid-cols-2',
  3: 'grid-cols-2 sm:grid-cols-3',
  4: 'grid-cols-3 sm:grid-cols-4',
  5: 'grid-cols-3 sm:grid-cols-4 md:grid-cols-5',
  6: 'grid-cols-3 sm:grid-cols-4 md:grid-cols-6',
}

/** 工具栏下拉菜单：自管开合、点外部或 Esc 关闭。 */
function ToolbarMenu({
  trigger,
  title,
  disabled = false,
  width = 'w-44',
  children,
}: {
  trigger: ReactNode
  title?: string
  disabled?: boolean
  width?: string
  children: (close: () => void) => ReactNode
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        title={title}
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1.5 whitespace-nowrap rounded-xl border border-[var(--app-border)] px-3 py-1.5 text-xs font-bold text-[var(--app-muted)] transition hover:text-[var(--app-text)] disabled:opacity-60"
      >
        {trigger}
      </button>
      {open && (
        <div
          role="menu"
          className={`absolute right-0 z-30 mt-1.5 origin-top-right rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-1 shadow-xl ${width}`}
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  )
}

function MenuItem({
  icon,
  children,
  onClick,
  disabled = false,
}: {
  icon?: ReactNode
  children: ReactNode
  onClick: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="menuitem"
      disabled={disabled}
      onClick={onClick}
      className="flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-2.5 py-2 text-left text-xs font-bold text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] disabled:opacity-60"
    >
      {icon}
      {children}
    </button>
  )
}

export function ReaderHomeContent({ embedded = false }: { embedded?: boolean }) {
  const navigate = useNavigate()
  const isAdmin = useAuthStore((state) => state.user?.role === 'admin')
  const [books, setBooks] = useState<ReaderBook[] | null>(null)
  const [error, setError] = useState('')
  const [uploading, setUploading] = useState<number | null>(null) // 上传进度百分比
  const [picker, setPicker] = useState<'book' | 'audio' | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [manageGroupsOpen, setManageGroupsOpen] = useState(false)
  const [groupPickerBook, setGroupPickerBook] = useState<ReaderBook | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  const shelfLayout = useReaderSettingsStore((s) => s.shelfLayout)
  const shelfSort = useReaderSettingsStore((s) => s.shelfSort)
  const setShelfSort = useReaderSettingsStore((s) => s.setShelfSort)
  const shelfGridColumns = useReaderSettingsStore((s) => s.shelfGridColumns)
  const shelfShowUnread = useReaderSettingsStore((s) => s.shelfShowUnread)
  const shelfShowUpdateTime = useReaderSettingsStore((s) => s.shelfShowUpdateTime)

  const sortedBooks = useMemo(() => (books ? sortShelfBooks(books, shelfSort) : null), [books, shelfSort])
  const sortLabel = SHELF_SORT_OPTIONS.find((o) => o.value === shelfSort)?.label ?? '最近阅读'

  // ── 书架分组（仿影视模块的媒体库标签）──
  const bookGroups = useBookGroups()
  const groupTabs = useMemo(
    () => buildBookGroupTabs(books ?? [], bookGroups.groups),
    [books, bookGroups.groups],
  )
  // 先按分组过滤，再按当前排序方式排；两者互不影响。
  const visibleBooks = useMemo(
    () => (sortedBooks ? filterBooksByGroup(sortedBooks, bookGroups.groups, bookGroups.selectedGroupId) : null),
    [sortedBooks, bookGroups.groups, bookGroups.selectedGroupId],
  )
  // 管理弹窗里每个分组的书籍数只统计书架上真实存在的书。
  const groupBookCounts = useMemo(() => {
    const counts: Record<string, number> = {}
    const available = new Set((books ?? []).map((b) => b.id))
    for (const group of bookGroups.groups) {
      counts[group.name] = group.book_ids.filter((id) => available.has(id)).length
    }
    return counts
  }, [books, bookGroups.groups])

  const load = () => {
    setError('')
    readerAPI
      .listBooks()
      .then(setBooks)
      .catch((e) => setError(e?.response?.data?.error ?? '加载书架失败'))
  }

  useEffect(load, [])

  const upload = async (file: File) => {
    setUploading(0)
    try {
      const book = await readerAPI.uploadLocalBook(file, setUploading)
      toast.success(`已导入《${book.name}》，共 ${book.total_chapter_num} 章`)
      load()
      navigate(`/reader/view/${book.id}`)
    } catch (e) {
      toast.error((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '导入失败')
    } finally {
      setUploading(null)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  // 移出书架：所有书籍都可移除。本地导入的书会连同服务器文件一起删除，
  // 原地引用的只解除引用，书源书籍只清掉书架记录与阅读进度。
  const removeFromShelf = async (book: ReaderBook) => {
    const message = book.local_external
      ? `《${book.name}》是原地引用服务器上的文件，移出书架只解除引用，不会删除源文件。确定吗？`
      : book.is_local
        ? `《${book.name}》及其在服务器上的文件都会被删除，确定吗？`
        : `《${book.name}》会连同阅读进度一起从书架移除，不影响书源。确定吗？`
    const ok = await confirmAction({
      title: '移出书架',
      message,
      confirmText: '移出',
      danger: true,
    })
    if (!ok) return
    try {
      await readerAPI.removeBook(book.id)
      setBooks((prev) => (prev ? prev.filter((b) => b.id !== book.id) : prev))
      toast.success('已移出书架')
    } catch {
      toast.error('移出失败')
    }
  }

  const runPicker = async (path: string) => {
    const kind = picker
    setPicker(null)
    try {
      const book =
        kind === 'audio'
          ? await readerAPI.importLocalAudioDir(path)
          : await readerAPI.importLocalBookFromPath(path)
      toast.success(`已导入《${book.name}》，共 ${book.total_chapter_num} 章`)
      load()
      navigate(`/reader/view/${book.id}`)
    } catch (e) {
      toast.error((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '导入失败')
    }
  }

  // 更新目录（对应 legado 书架的「更新目录」）：重抓全部网络书籍的目录，刷出最新章节。
  const refreshToc = async () => {
    setRefreshing(true)
    const toastId = toast.loading('正在更新目录…')
    try {
      const res = await readerAPI.refreshBooksToc()
      load()
      if (res.total === 0) {
        toast.success('没有需要更新的网络书籍', { id: toastId })
      } else if (res.failed > 0) {
        toast(`更新完成：${res.updated} 本有新章节，${res.failed} 本失败`, { id: toastId, icon: '⚠️' })
      } else {
        toast.success(res.updated > 0 ? `更新完成，${res.updated} 本有新章节` : '已是最新目录', { id: toastId })
      }
    } catch (e) {
      toast.error((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '更新目录失败', {
        id: toastId,
      })
    } finally {
      setRefreshing(false)
    }
  }

  const assignBookToGroup = async (book: ReaderBook, groupName: string) => {
    setGroupPickerBook(null)
    await bookGroups.assignBook(book.id, groupName)
  }

  const inSpecificGroup = bookGroups.selectedGroupId !== ALL_GROUP_ID

  return (
    <div className="space-y-6">
      {!embedded && <ReaderModeSwitch />}

      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="shrink-0 font-display text-2xl text-ink-600">
          书架
          {visibleBooks !== null && visibleBooks.length > 0 && (
            <span className="ml-2 align-middle text-xs font-normal text-[var(--app-muted)]">
              {visibleBooks.length} 本
              {inSpecificGroup && books !== null && <span className="ml-1">/ 共 {books.length} 本</span>}
            </span>
          )}
        </h1>

        <div className="flex items-center gap-2">
          <input
            ref={fileRef}
            type="file"
            accept=".txt,.epub,text/plain,application/epub+zip"
            className="hidden"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) void upload(f)
            }}
          />

          {/* 排序（对应 legado 书架设置的排序项） */}
          <ToolbarMenu
            title="排序方式"
            trigger={
              <>
                <SlidersHorizontal size={13} />
                <span className="hidden sm:inline">{sortLabel}</span>
                <ChevronDown size={13} />
              </>
            }
          >
            {(close) => (
              <>
                {SHELF_SORT_OPTIONS.map((option) => (
                  <MenuItem
                    key={option.value}
                    icon={shelfSort === option.value ? <Check size={13} /> : <span className="w-[13px]" />}
                    onClick={() => {
                      setShelfSort(option.value)
                      close()
                    }}
                  >
                    {option.label}
                  </MenuItem>
                ))}
              </>
            )}
          </ToolbarMenu>

          {/* 布局 / 显示设置 */}
          <button
            type="button"
            aria-label="书架设置"
            title="书架设置：布局、排序、显示项"
            onClick={() => setSettingsOpen(true)}
            className="flex items-center gap-1.5 whitespace-nowrap rounded-xl border border-[var(--app-border)] px-3 py-1.5 text-xs font-bold text-[var(--app-muted)] transition hover:text-[var(--app-text)]"
          >
            {shelfLayout === 'grid' ? <LayoutGrid size={14} /> : shelfLayout === 'list' ? <List size={14} /> : <Rows3 size={14} />}
            <Settings2 size={13} />
          </button>

          {/* 书架操作 */}
          <ToolbarMenu
            title="书架操作"
            width="w-44"
            disabled={uploading !== null}
            trigger={
              <>
                {uploading !== null ? <Loader2 size={13} className="animate-spin" /> : <MoreHorizontal size={14} />}
                <span className="hidden sm:inline">{uploading !== null ? `上传中 ${uploading}%` : '管理'}</span>
                <ChevronDown size={13} />
              </>
            }
          >
            {(close) => (
              <>
                <MenuItem
                  icon={<FileUp size={13} />}
                  disabled={uploading !== null}
                  onClick={() => {
                    close()
                    fileRef.current?.click()
                  }}
                >
                  本地导入
                </MenuItem>
                {isAdmin && (
                  <>
                    <MenuItem
                      icon={<FolderOpen size={13} />}
                      disabled={uploading !== null}
                      onClick={() => {
                        close()
                        setPicker('book')
                      }}
                    >
                      服务器导入
                    </MenuItem>
                    <MenuItem
                      icon={<Headphones size={13} />}
                      disabled={uploading !== null}
                      onClick={() => {
                        close()
                        setPicker('audio')
                      }}
                    >
                      有声书导入
                    </MenuItem>
                  </>
                )}
                <MenuItem
                  icon={refreshing ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}
                  disabled={refreshing}
                  onClick={() => {
                    close()
                    void refreshToc()
                  }}
                >
                  更新目录
                </MenuItem>
                <MenuItem
                  icon={<SlidersHorizontal size={13} />}
                  onClick={() => {
                    close()
                    setSettingsOpen(true)
                  }}
                >
                  书架设置
                </MenuItem>
                <MenuItem
                  icon={<Settings2 size={13} />}
                  onClick={() => {
                    close()
                    setManageGroupsOpen(true)
                  }}
                >
                  管理分组
                </MenuItem>
                <div className="my-1 border-t border-[var(--app-border)]" />
                <Link
                  to="/reader/sources"
                  role="menuitem"
                  onClick={close}
                  className="flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-2.5 py-2 text-left text-xs font-bold text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
                >
                  <Settings2 size={13} /> 书源管理
                </Link>
              </>
            )}
          </ToolbarMenu>
        </div>
      </div>

      {books !== null && books.length > 0 && (
        <BookGroupBar
          tabs={groupTabs}
          selectedGroupId={bookGroups.selectedGroupId}
          onSelect={bookGroups.setSelectedGroupId}
        />
      )}

      {books === null && !error && (
        <div className="flex items-center justify-center py-24 text-[var(--app-muted)]">
          <Loader2 className="animate-spin" size={22} />
        </div>
      )}

      {error && books === null && (
        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-8 text-center">
          <p className="text-sm text-[var(--app-muted)]">{error}</p>
          <button type="button" onClick={load} className="mt-4 inline-flex items-center gap-1.5 btn-outline text-xs">
            <RefreshCw size={13} /> 重试
          </button>
        </div>
      )}

      {books !== null && books.length === 0 && (
        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-12 text-center">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)]">
            <BookOpen size={20} className="text-brand-500" />
          </div>
          <p className="mt-4 text-sm font-bold text-[var(--app-text)]">书架空空如也</p>
          <p className="mt-1 text-xs text-[var(--app-muted)]">上传本地 TXT / EPUB，或导入书源后搜索加入书架</p>
          <div className="mt-5 flex items-center justify-center gap-3">
            <button
              type="button"
              disabled={uploading !== null}
              onClick={() => fileRef.current?.click()}
              className="btn-outline text-xs disabled:opacity-60"
            >
              {uploading !== null ? `上传中 ${uploading}%` : '上传本地书籍'}
            </button>
            <Link to="/reader/sources" className="btn-outline text-xs">导入书源</Link>
            <Link to="/reader/search" className="btn-primary text-xs">去搜索</Link>
          </div>
          {isAdmin && (
            <div className="mt-3 flex flex-wrap items-center justify-center gap-3">
              <button type="button" onClick={() => setPicker('book')} className="btn-outline whitespace-nowrap text-xs">
                从服务器导入
              </button>
              <button type="button" onClick={() => setPicker('audio')} className="btn-outline whitespace-nowrap text-xs">
                从服务器导入有声书
              </button>
            </div>
          )}
        </div>
      )}

      {books !== null && books.length > 0 && visibleBooks !== null && visibleBooks.length === 0 && (
        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-10 text-center">
          <p className="text-sm font-bold text-[var(--app-text)]">这个分组还没有书</p>
          <p className="mt-1 text-xs text-[var(--app-muted)]">
            在书籍卡片上点「移到分组」把书放进来，或切到「未分组」看看还没归组的书
          </p>
        </div>
      )}

      {visibleBooks !== null && visibleBooks.length > 0 && (
        shelfLayout === 'grid' ? (
          <div className={`grid gap-x-4 gap-y-6 ${GRID_COLUMNS_CLASS[shelfGridColumns] ?? GRID_COLUMNS_CLASS[0]}`}>
            {visibleBooks.map((book) => (
              <BookshelfCard
                key={book.id}
                book={book}
                layout="grid"
                showUnread={shelfShowUnread}
                showUpdateTime={shelfShowUpdateTime}
                onOpen={() => navigate(`/reader/view/${book.id}`)}
                onPickGroup={() => setGroupPickerBook(book)}
                onRemove={() => void removeFromShelf(book)}
              />
            ))}
          </div>
        ) : (
          <div className="space-y-2">
            {visibleBooks.map((book) => (
              <BookshelfCard
                key={book.id}
                book={book}
                layout={shelfLayout}
                showUnread={shelfShowUnread}
                showUpdateTime={shelfShowUpdateTime}
                groupName={groupIdOfBook(bookGroups.groups, book.id)}
                onOpen={() => navigate(`/reader/view/${book.id}`)}
                onPickGroup={() => setGroupPickerBook(book)}
                onRemove={() => void removeFromShelf(book)}
              />
            ))}
          </div>
        )
      )}

      {settingsOpen && <BookshelfSettingsDialog onClose={() => setSettingsOpen(false)} />}

      {manageGroupsOpen && (
        <ManageBookGroupsDialogView
          groups={bookGroups.groups}
          bookCounts={groupBookCounts}
          saving={bookGroups.saving}
          onCreate={bookGroups.createGroup}
          onRename={bookGroups.renameGroup}
          onRemove={bookGroups.removeGroup}
          onReorder={bookGroups.reorderGroups}
          onClose={() => setManageGroupsOpen(false)}
        />
      )}

      {groupPickerBook && (
        <BookGroupPickerDialog
          book={groupPickerBook}
          groups={bookGroups.groups}
          currentGroupId={groupIdOfBook(bookGroups.groups, groupPickerBook.id)}
          onPick={(groupName) => void assignBookToGroup(groupPickerBook, groupName)}
          onClose={() => setGroupPickerBook(null)}
        />
      )}

      {picker && (
        <ServerFilePickerDialog
          mode={picker === 'audio' ? 'dir' : 'file'}
          extensions={picker === 'audio' ? undefined : ['.txt', '.epub']}
          title={picker === 'audio' ? '选择有声书目录（音频文件与 .strm）' : '选择服务器上的书籍文件（TXT / EPUB）'}
          onSelect={(path) => void runPicker(path)}
          onClose={() => setPicker(null)}
        />
      )}
    </div>
  )
}
