import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import toast from 'react-hot-toast'
import { AlertTriangle, ArrowLeft, ChevronDown, ChevronRight, Loader2, Plus, Search } from 'lucide-react'

import { readerAPI, type ReaderSearchBook, type ReaderSearchSkipped } from '../../api/reader'
import ReaderBookCover from '../../components/ReaderBookCover'
import { splitKindTags } from '../../utils/kindTags'
import { SourcePickerDialog } from './SourcePickerDialog'

// 多源聚合搜索页（仿 legado SearchActivity：结果流 + 失败书源列表）。

export default function ReaderSearchPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const [key, setKey] = useState(() => params.get('key') ?? '')
  const [searching, setSearching] = useState(false)
  // 换源：点「N 源可换」后选择用哪个源打开这本书
  const [picker, setPicker] = useState<ReaderSearchBook | null>(null)
  const [books, setBooks] = useState<ReaderSearchBook[] | null>(null)
  const [skipped, setSkipped] = useState<ReaderSearchSkipped[]>([])
  const [showSkipped, setShowSkipped] = useState(false)
  const [adding, setAdding] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  const doSearch = async () => {
    const kw = key.trim()
    if (!kw || searching) return
    setSearching(true)
    setShowSkipped(false)
    try {
      const res = await readerAPI.search(kw)
      setBooks(res.books ?? [])
      // 后端在「没有书源失败」时会把空列表编码成 null，这里兜底成数组，
      // 否则下面 skipped.length 会直接抛 TypeError 把整页打崩。
      setSkipped(res.skipped ?? [])
      if ((res.books ?? []).length === 0) toast.error('所有书源都没有找到结果')
    } catch (e) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '搜索失败'
      toast.error(msg)
    } finally {
      setSearching(false)
    }
  }

// 从首页顶部书搜索带 ?key= 进来时自动搜一次（只做一次，之后由用户手动搜）
  const autoSearchedRef = useRef(false)
  useEffect(() => {
    if (autoSearchedRef.current) return
    autoSearchedRef.current = true
    if (key.trim()) void doSearch()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const addToShelf = async (book: ReaderSearchBook) => {    const origin = book.origins[0]
    if (!origin) return
    setAdding(book.book_url)
    try {
      await readerAPI.addBook({ origin, name: book.name, author: book.author, cover_url: book.cover_url })
      toast.success(`《${book.name}》已加入书架`)
    } catch (e) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '加入书架失败'
      toast.error(msg)
    } finally {
      setAdding('')
    }
  }

  const openBook = (book: ReaderSearchBook, origin = book.origins[0]) => {
    if (!origin) return
    navigate(
      `/reader/book?${new URLSearchParams({
        source_url: origin.origin,
        book_url: origin.book_url,
        name: book.name,
        author: book.author,
        cover_url: book.cover_url,
        origin_id: origin.source_id,
        origin_name: origin.origin_name,
      }).toString()}`,
    )
  }

  return (
    <div className="mx-auto min-h-[100dvh] w-full max-w-4xl px-4 pb-16 pt-4 sm:px-6">
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={() => navigate(-1)}
          className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
        >
          <ArrowLeft size={18} />
        </button>
        <div className="flex flex-1 items-center gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] px-3 py-2">
          <Search size={15} className="text-[var(--app-muted)]" />
          <input
            ref={inputRef}
            autoFocus
            value={key}
            onChange={(e) => setKey(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && doSearch()}
            placeholder="搜索书名或作者"
            className="w-full bg-transparent text-sm text-[var(--app-text)] outline-none placeholder:text-[var(--app-muted)]"
          />
        </div>
        <button
          type="button"
          onClick={doSearch}
          disabled={searching || !key.trim()}
          className="btn-primary text-xs disabled:opacity-50"
        >
          {searching ? <Loader2 size={13} className="animate-spin" /> : '搜索'}
        </button>
      </div>

      {searching && (
        <div className="flex flex-col items-center gap-2 py-24 text-[var(--app-muted)]">
          <Loader2 className="animate-spin" size={22} />
          <p className="text-xs">正在并发搜索启用的书源（单源最长 30 秒）…</p>
        </div>
      )}

      {!searching && books !== null && (
        <div className="mt-6 space-y-3">
          {books.map((book) => (
            <div
              key={`${book.name}|${book.author}`}
              className="flex gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4"
            >
              <button type="button" onClick={() => openBook(book)} className="shrink-0">
                <div className="h-24 w-16 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-panel-soft)]">
                  <ReaderBookCover url={book.cover_url} alt={book.name} iconSize={18} />
                </div>
              </button>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <button type="button" onClick={() => openBook(book)} className="min-w-0 text-left">
                    <p className="truncate text-sm font-bold text-[var(--app-text)] hover:text-brand-600">{book.name}</p>
                  </button>
                  {book.origins.length > 1 && (
                    <button
                      type="button"
                      onClick={() => setPicker(book)}
                      title="换源"
                      className="shrink-0 rounded-md bg-brand-500/10 px-1.5 py-0.5 text-2xs font-bold text-brand-600 transition hover:bg-brand-500/20"
                    >
                      {book.origins.length} 源可换
                    </button>
                  )}
                </div>
                <p className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-[var(--app-muted)]">
                  <span className="truncate">{book.author || '佚名'}</span>
                  {splitKindTags(book.kind).map((k) => (
                    <span key={k} className="shrink-0 rounded bg-brand-500/10 px-1 py-0.5 text-2xs font-bold text-brand-600">
                      {k}
                    </span>
                  ))}
                  {book.word_count && (
                    <span className="shrink-0 rounded bg-[var(--app-hover)] px-1 py-0.5 text-2xs font-bold text-[var(--app-muted)]">
                      {book.word_count}
                    </span>
                  )}
                </p>
                <p className="mt-1 truncate text-xs text-[var(--app-muted)]">最新：{book.latest_chapter || '未知'}</p>
                {book.intro && <p className="mt-1 line-clamp-2 text-xs text-[var(--app-subtle)]">{book.intro}</p>}
              </div>
              <div className="flex shrink-0 flex-col justify-center">
                <button
                  type="button"
                  onClick={() => addToShelf(book)}
                  disabled={adding === book.book_url}
                  className="btn-outline text-2xs"
                >
                  {adding === book.book_url ? <Loader2 size={12} className="animate-spin" /> : <Plus size={12} />}
                  加书架
                </button>
              </div>
            </div>
          ))}

          {skipped.length > 0 && (
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel-soft)]">
              <button
                type="button"
                onClick={() => setShowSkipped((v) => !v)}
                className="flex w-full items-center gap-2 px-4 py-3 text-xs font-bold text-[var(--app-muted)]"
              >
                <AlertTriangle size={13} className="text-amber-500" />
                {skipped.length} 个书源搜索失败
                {showSkipped ? <ChevronDown size={13} className="ml-auto" /> : <ChevronRight size={13} className="ml-auto" />}
              </button>
              {showSkipped && (
                <div className="space-y-1.5 px-4 pb-3">
                  {skipped.map((s) => (
                    <div key={s.source_id} className="flex items-start gap-2 text-xs text-[var(--app-muted)]">
                      <span className="shrink-0 font-bold text-[var(--app-text)]">{s.origin_name}</span>
                      <span className="min-w-0 break-all">{s.reason}</span>
                    </div>
                  ))}
                  <Link to="/reader/sources" className="inline-block pt-1 text-2xs text-brand-600 hover:underline">
                    去书源管理排查 →
                  </Link>
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {!searching && books === null && (
        <div className="py-24 text-center text-xs text-[var(--app-muted)]">
          输入关键词开始搜索 · 需要 <Link to="/reader/sources" className="text-brand-600 hover:underline">先导入书源</Link>
        </div>
      )}

      {picker && (
        <SourcePickerDialog
          title={picker.name}
          origins={picker.origins}
          onPick={(origin) => {
            const book = picker
            setPicker(null)
            openBook(book, origin)
          }}
          onClose={() => setPicker(null)}
        />
      )}
    </div>
  )
}
