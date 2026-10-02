import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import toast from 'react-hot-toast'
import { AlertTriangle, ArrowLeft, ChevronDown, ChevronRight, ListFilter, Loader2, Plus, Search } from 'lucide-react'

import { readerAPI, type ReaderSearchBook, type ReaderSearchSkipped, type ReaderSource } from '../../api/reader'
import ReaderBookCover from '../../components/ReaderBookCover'
import { useReaderSettingsStore } from '../../stores/readerSettings'
import { splitKindTags } from '../../utils/kindTags'
import { SearchScopeDialog } from './SearchScopeDialog'
import { SourcePickerDialog } from './SourcePickerDialog'

// 多源聚合搜索页（仿 legado SearchActivity：结果流 + 失败书源列表）。
// 搜索前可按书源收敛范围（对应 legado 的搜索范围）：默认全选已启用书源。
// 搜索结果会剔除已在书架里的书——这里的搜索是用来发现新书的。

/** 书架命中键：与搜索结果一致按「书名 + 作者」聚合；作者缺省时只用书名（同 legado isInBookShelf）。 */
function shelfBookKey(name: string, author: string): string {
  const n = name.trim()
  const a = author.trim()
  return a ? `${n}|${a}` : n
}

/** 这本书是否已在书架：优先按书名+作者，其次按书本身/任一命中书源的地址（换源后地址会变）。 */
function isOnShelf(book: ReaderSearchBook, shelf: Set<string>): boolean {
  if (shelf.has(shelfBookKey(book.name, book.author))) return true
  if (book.book_url && shelf.has(book.book_url)) return true
  return book.origins.some((o) => !!o.book_url && shelf.has(o.book_url))
}

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

  // 搜索范围（设备级持久化，见 readerSettings store）：空数组 = 全部启用书源。
  const scopeIds = useReaderSettingsStore((s) => s.searchScopeIds)
  const setSearchScopeIds = useReaderSettingsStore((s) => s.setSearchScopeIds)
  const [sources, setSources] = useState<ReaderSource[]>([])
  const [showScope, setShowScope] = useState(false)
  const enabledSources = useMemo(() => sources.filter((s) => s.enabled), [sources])
  // 已保存范围里仍有效的书源（删源/停源后自动剔除，全失效时等同于全部）。
  const activeScopeIds = useMemo(
    () => scopeIds.filter((id) => enabledSources.some((s) => s.id === id)),
    [scopeIds, enabledSources],
  )
  // 书源列表还没加载完（sources 为空）时不能判断有效性，先按已存 ID 数显示，
  // 避免已保存子集在加载瞬间被错标成「全部书源」。
  const scopeAll = scopeIds.length === 0 || (sources.length > 0 && activeScopeIds.length === 0)
  const scopeLabel = scopeAll ? '全部书源' : `已选 ${activeScopeIds.length || scopeIds.length} 个书源`

  useEffect(() => {
    readerAPI.listSources().then(setSources).catch(() => undefined)
  }, [])

  // 书架命中集合：已在书架的书不参与结果展示（这里只找新书）。
  const [shelfKeys, setShelfKeys] = useState<Set<string>>(() => new Set())
  useEffect(() => {
    readerAPI
      .listBooks()
      .then((books) => {
        const keys = new Set<string>()
        for (const b of books) {
          keys.add(shelfBookKey(b.name, b.author))
          if (b.book_url) keys.add(b.book_url)
        }
        setShelfKeys(keys)
      })
      .catch(() => undefined)
  }, [])

  // 展示用结果 = 原始结果剔除已在书架的书。用派生值而不是在 doSearch 里过滤：
  // 书架是异步加载的，派生能保证「书架先到还是结果先到」都能正确隐藏，
  // 且刚加进书架的书也会立刻从列表消失。
  const visibleBooks = useMemo(
    () => (books === null ? null : books.filter((b) => !isOnShelf(b, shelfKeys))),
    [books, shelfKeys],
  )
  const hiddenOnShelf = books === null ? 0 : books.length - (visibleBooks?.length ?? 0)

  const doSearch = async () => {
    const kw = key.trim()
    if (!kw || searching) return
    setSearching(true)
    setShowSkipped(false)
    try {
      // 直接把已保存范围交给后端：它只搜其中仍启用的书源，全部失效时退回全部启用，
      // 因此这里不必等书源列表加载完，也能正确处理「换设备后书源尚未同步」的情况。
      const res = await readerAPI.search(kw, scopeIds)
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

  const addToShelf = async (book: ReaderSearchBook) => {
    const origin = book.origins[0]
    if (!origin) return
    setAdding(book.book_url)
    try {
      await readerAPI.addBook({ origin, name: book.name, author: book.author, cover_url: book.cover_url })
      toast.success(`《${book.name}》已加入书架`)
      // 立刻并入书架命中集合：这本「刚加的书」应从「搜索新书」的结果里消失。
      setShelfKeys((prev) => {
        const next = new Set(prev)
        next.add(shelfBookKey(book.name, book.author))
        if (origin.book_url) next.add(origin.book_url)
        return next
      })
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

      {/* 搜索范围：默认「全部书源」（全选已启用），点开可收敛到指定书源 */}
      <div className="mt-3 flex items-center gap-2">
        <button
          type="button"
          onClick={() => setShowScope(true)}
          className="inline-flex items-center gap-1.5 rounded-full border border-[var(--app-border)] bg-[var(--app-panel)] px-3 py-1.5 text-2xs font-bold text-[var(--app-muted)] transition hover:border-brand-500/40 hover:text-brand-600"
        >
          <ListFilter size={13} />
          搜索范围
          <span className="text-brand-600">{scopeLabel}</span>
        </button>
        {scopeAll && enabledSources.length > 0 && (
          <span className="text-2xs text-[var(--app-subtle)]">共 {enabledSources.length} 个启用书源</span>
        )}
      </div>

      {searching && (
        <div className="flex flex-col items-center gap-2 py-24 text-[var(--app-muted)]">
          <Loader2 className="animate-spin" size={22} />
          <p className="text-xs">正在并发搜索启用的书源（单源最长 30 秒）…</p>
        </div>
      )}

      {!searching && visibleBooks !== null && (
        <div className="mt-6 space-y-3">
          {hiddenOnShelf > 0 && visibleBooks.length > 0 && (
            <p className="rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-2xs text-[var(--app-muted)]">
              已隐藏 {hiddenOnShelf} 本已在书架里的书
            </p>
          )}
          {visibleBooks.length === 0 && (
            <p className="py-16 text-center text-xs text-[var(--app-muted)]">
              {hiddenOnShelf > 0 ? '搜到的书都已经在书架里了' : '没有找到结果'}
            </p>
          )}
          {visibleBooks.map((book) => (
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

      {showScope && (
        <SearchScopeDialog
          sources={sources}
          selectedIds={scopeIds}
          onConfirm={(ids) => {
            setSearchScopeIds(ids)
            setShowScope(false)
          }}
          onClose={() => setShowScope(false)}
        />
      )}
    </div>
  )
}
