import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import toast from 'react-hot-toast'
import { BookOpen, ChevronDown, FileUp, FolderOpen, HardDrive, Headphones, Loader2, MoreHorizontal, RefreshCw, Settings2, Trash2 } from 'lucide-react'

import { readerAPI, type ReaderBook } from '../../api/reader'
import { confirmAction } from '../../components/confirmAction'
import ReaderBookCover from '../../components/ReaderBookCover'
import { useAuthStore } from '../../stores/auth'
import { ReaderModeSwitch } from './ReaderModeSwitch'
import { ServerFilePickerDialog } from './ServerFilePickerDialog'

// 首页阅读模式的书架内容（首页切换与 /reader 路由共用）。
// 结构仿 legado 书架：网格封面 + 书名 + 阅读进度，右上上传本地书籍/搜索/书源管理入口。

// 未读章数：dur_chapter_time 为 0 表示还没开始读，否则读完到当前章为止。
// total_chapter_num 为 0 表示目录尚未缓存，无法计算。
function unreadChapters(book: ReaderBook): number | null {
  if (book.total_chapter_num <= 0) return null
  const read = book.dur_chapter_time > 0 ? book.dur_chapter_index + 1 : 0
  return Math.max(0, Math.min(book.total_chapter_num, book.total_chapter_num - read))
}

export function ReaderHomeContent({ embedded = false }: { embedded?: boolean }) {
  const navigate = useNavigate()
  const isAdmin = useAuthStore((state) => state.user?.role === 'admin')
  const [books, setBooks] = useState<ReaderBook[] | null>(null)
  const [error, setError] = useState('')
  const [uploading, setUploading] = useState<number | null>(null) // 上传进度百分比
  const [picker, setPicker] = useState<'book' | 'audio' | null>(null)
  const [menuOpen, setMenuOpen] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  // 下拉菜单：点外部或按 Esc 关闭
  useEffect(() => {
    if (!menuOpen) return
    const onPointerDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setMenuOpen(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMenuOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [menuOpen])

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

  return (
    <div className="space-y-6">
      {!embedded && <ReaderModeSwitch />}

      {/* 标题右侧一个下拉入口，收纳全部书架操作 */}
      <div className="flex items-center justify-between gap-3">
        <h1 className="shrink-0 font-display text-2xl text-ink-600">书架</h1>
        <div className="relative" ref={menuRef}>
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
          <button
            type="button"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen((v) => !v)}
            className="flex items-center gap-1.5 whitespace-nowrap rounded-xl border border-[var(--app-border)] px-3 py-1.5 text-xs font-bold text-[var(--app-muted)] hover:text-[var(--app-text)]"
            title="书架操作"
          >
            {uploading !== null ? <Loader2 size={13} className="animate-spin" /> : <MoreHorizontal size={14} />}
            {uploading !== null ? `上传中 ${uploading}%` : '管理'}
            <ChevronDown size={13} className={`transition-transform ${menuOpen ? 'rotate-180' : ''}`} />
          </button>

          {menuOpen && (
            <div
              role="menu"
              className="absolute right-0 z-30 mt-1.5 w-40 origin-top-right rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-1 shadow-xl"
            >
              <button
                type="button"
                role="menuitem"
                disabled={uploading !== null}
                title="上传 TXT / EPUB 到服务器阅读"
                onClick={() => {
                  setMenuOpen(false)
                  fileRef.current?.click()
                }}
                className="flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-2.5 py-2 text-left text-xs font-bold text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] disabled:opacity-60"
              >
                <FileUp size={13} /> 本地导入
              </button>
              {isAdmin && (
                <>
                  <button
                    type="button"
                    role="menuitem"
                    disabled={uploading !== null}
                    title="选择服务器上已有的 TXT / EPUB 文件导入（原地引用，不复制）"
                    onClick={() => {
                      setMenuOpen(false)
                      setPicker('book')
                    }}
                    className="flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-2.5 py-2 text-left text-xs font-bold text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] disabled:opacity-60"
                  >
                    <FolderOpen size={13} /> 服务器导入
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    disabled={uploading !== null}
                    title="选择服务器上的一个目录导入为有声书（含 .strm 播放指针）"
                    onClick={() => {
                      setMenuOpen(false)
                      setPicker('audio')
                    }}
                    className="flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-2.5 py-2 text-left text-xs font-bold text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] disabled:opacity-60"
                  >
                    <Headphones size={13} /> 有声书导入
                  </button>
                </>
              )}
              <div className="my-1 border-t border-[var(--app-border)]" />
              <Link
                to="/reader/sources"
                role="menuitem"
                onClick={() => setMenuOpen(false)}
                className="flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-2.5 py-2 text-left text-xs font-bold text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
              >
                <Settings2 size={13} /> 书源管理
              </Link>
            </div>
          )}
        </div>
      </div>

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

      {books !== null && books.length > 0 && (
        <div className="grid grid-cols-3 gap-x-4 gap-y-6 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-8">
          {books.map((book) => {
            const unread = unreadChapters(book)
            return (
              <div key={book.id} className="group">
                <div
                  role="button"
                  tabIndex={0}
                  onClick={() => navigate(`/reader/view/${book.id}`)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      navigate(`/reader/view/${book.id}`)
                    }
                  }}
                  className="relative w-full cursor-pointer overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] shadow-sm transition group-hover:shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-500"
                >
                  <div className="aspect-[3/4] w-full">
                    <ReaderBookCover url={book.cover_url} alt={book.name} iconSize={22} />
                  </div>
                  {/* 左下角：本地来源标记（右上角留给未读徽标，右下角是移出按钮，避免窄卡片时重叠） */}
                  {book.is_local && (
                    <span className="absolute bottom-1 left-1 flex items-center gap-1 whitespace-nowrap rounded-lg bg-black/55 px-1.5 py-0.5 text-[10px] font-bold text-white backdrop-blur">
                      <HardDrive size={10} /> 本地
                    </span>
                  )}
                  {unread !== null && (
                    <span
                      title={unread > 0 ? `还有 ${unread} 章未读` : '已读完'}
                      className={`absolute right-1 top-1 whitespace-nowrap rounded-lg px-1.5 py-0.5 text-[10px] font-bold text-white backdrop-blur ${
                        unread > 0 ? 'bg-rose-500/90' : 'bg-black/55'
                      }`}
                    >
                      {unread > 0 ? `${unread} 章未读` : '已读完'}
                    </span>
                  )}
                  <button
                    type="button"
                    aria-label={`移出书架：${book.name}`}
                    title="移出书架"
                    onClick={(e) => {
                      e.stopPropagation()
                      void removeFromShelf(book)
                    }}
                    className="absolute bottom-1 right-1 rounded-lg bg-black/55 p-1 text-white opacity-80 backdrop-blur transition hover:bg-red-500 hover:opacity-100 focus-visible:opacity-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-white"
                  >
                    <Trash2 size={11} />
                  </button>
                </div>
                <p className="mt-2 truncate text-xs font-bold text-[var(--app-text)]">{book.name}</p>
                <p className="truncate text-2xs text-[var(--app-muted)]">
                  {book.dur_chapter_title ? `读到 ${book.dur_chapter_title}` : book.author || '未开始阅读'}
                </p>
              </div>
            )
          })}
        </div>
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
