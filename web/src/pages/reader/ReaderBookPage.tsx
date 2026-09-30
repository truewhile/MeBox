import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import toast from 'react-hot-toast'
import { ArrowLeft, BookOpen, ChevronDown, ChevronUp, Loader2, RefreshCw, Trash2 } from 'lucide-react'

import { readerAPI, type ReaderBook, type ReaderBookInfo, type ReaderTocChapter } from '../../api/reader'

// 书籍详情页（仿 legado BookInfoActivity：封面 + 信息 + 简介 + 目录入口 + 加书架/开始阅读）。

export default function ReaderBookPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const sourceURL = params.get('source_url') ?? ''
  const bookURL = params.get('book_url') ?? ''
  const [qName, qAuthor] = [params.get('name') ?? '', params.get('author') ?? '']
  const [qCover] = [params.get('cover_url') ?? '']
  const [originID] = [params.get('origin_id') ?? '']
  const [originName] = [params.get('origin_name') ?? '']

  const [info, setInfo] = useState<ReaderBookInfo | null>(null)
  const [infoError, setInfoError] = useState('')
  const [chapters, setChapters] = useState<ReaderTocChapter[] | null>(null)
  const [shelfBook, setShelfBook] = useState<ReaderBook | null>(null)
  const [busy, setBusy] = useState(false)
  const [tocExpanded, setTocExpanded] = useState(false)
  const [introExpanded, setIntroExpanded] = useState(false)

  const loadInfo = () => {
    if (!bookURL) return
    setInfoError('')
    readerAPI
      .bookInfo({ source_url: sourceURL, book_url: bookURL })
      .then(setInfo)
      .catch((e) => setInfoError(e?.response?.data?.error ?? '详情加载失败'))
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(loadInfo, [bookURL])

  // 书架状态 + 目录
  useEffect(() => {
    if (!bookURL) return
    readerAPI
      .listBooks()
      .then((books) => {
        const hit = books.find((b) => b.book_url === bookURL && b.origin === sourceURL) ?? null
        setShelfBook(hit)
      })
      .catch(() => undefined)
    readerAPI
      .toc({ source_url: sourceURL, book_url: bookURL, toc_url: bookURL })
      .then(setChapters)
      .catch(() => setChapters([]))
  }, [bookURL, sourceURL])

  // 详情返回的 tocUrl 更准确，拿到后重新拉目录
  useEffect(() => {
    const tocURL = info?.toc_url
    if (!tocURL || tocURL === bookURL) return
    readerAPI
      .toc({ source_url: sourceURL, book_url: bookURL, toc_url: tocURL })
      .then(setChapters)
      .catch(() => undefined)
  }, [info?.toc_url, sourceURL, bookURL])

  const name = info?.name || qName
  const author = info?.author || qAuthor
  const cover = info?.cover_url || qCover

  const firstReadableChapter = useMemo(() => chapters?.find((c) => !c.is_volume && c.url) ?? null, [chapters])

  const startReading = async () => {
    setBusy(true)
    try {
      let book = shelfBook
      if (!book) {
        book = await readerAPI.addBook({
          origin: {
            source_id: originID,
            origin: sourceURL,
            origin_name: originName || info?.name || '',
            origin_type: 0,
            book_url: bookURL,
            latest_chapter: info?.latest_chapter ?? '',
          },
          name,
          author,
          cover_url: cover,
        })
      }
      navigate(`/reader/view/${book.id}`)
    } catch (e) {
      toast.error((e as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '操作失败')
    } finally {
      setBusy(false)
    }
  }

  const removeFromShelf = async () => {
    if (!shelfBook) return
    try {
      await readerAPI.removeBook(shelfBook.id)
      toast.success('已移出书架')
      setShelfBook(null)
    } catch {
      toast.error('移出失败')
    }
  }

  const refreshToc = async () => {
    if (!info?.toc_url && !bookURL) return
    setBusy(true)
    try {
      const list = await readerAPI.toc({ source_url: sourceURL, book_url: bookURL, toc_url: info?.toc_url || bookURL })
      setChapters(list)
      toast.success(`目录已刷新，共 ${list.length} 章`)
    } catch {
      toast.error('目录刷新失败')
    } finally {
      setBusy(false)
    }
  }

  if (!bookURL) {
    return (
      <div className="mx-auto px-6 py-24 text-center text-sm text-[var(--app-muted)]">缺少书籍参数</div>
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
        <p className="flex-1 truncate text-sm font-bold text-[var(--app-text)]">{name}</p>
        <button
          type="button"
          onClick={refreshToc}
          disabled={busy}
          className="rounded-xl p-2 text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]"
          title="刷新目录"
        >
          <RefreshCw size={16} className={busy ? 'animate-spin' : ''} />
        </button>
      </div>

      {/* 信息区（仿 legado：封面 + 书名/作者/最新章节/简介） */}
      <div className="mt-6 flex gap-5">
        <div className="h-40 w-28 shrink-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)]">
          {cover ? (
            <img src={cover} alt={name} referrerPolicy="no-referrer" className="h-full w-full object-cover" />
          ) : (
            <div className="flex h-full w-full items-center justify-center">
              <BookOpen size={24} className="text-[var(--app-muted)]" />
            </div>
          )}
        </div>
        <div className="min-w-0 flex-1">
          <h1 className="font-display text-xl text-ink-600">{name || '未知书名'}</h1>
          <p className="mt-1 text-xs text-[var(--app-muted)]">{author || '佚名'}</p>
          {info?.kind && (
            <p className="mt-1 flex flex-wrap gap-1.5">
              {info.kind.split(/[,,]/).filter(Boolean).slice(0, 5).map((k) => (
                <span key={k} className="rounded-md bg-brand-500/10 px-1.5 py-0.5 text-2xs font-bold text-brand-600">
                  {k}
                </span>
              ))}
            </p>
          )}
          <p className="mt-1 truncate text-xs text-[var(--app-muted)]">
            最新：<span className="text-[var(--app-text)]">{info?.latest_chapter || '加载中…'}</span>
          </p>
          <p className="mt-0.5 text-2xs text-[var(--app-muted)]">书源：{originName || sourceURL}</p>
          {(info?.intro || infoError) && (
            <div className="mt-3">
              <p className={`text-xs leading-5 text-[var(--app-subtle)] ${introExpanded ? '' : 'line-clamp-3'}`}>
                {infoError || info?.intro}
              </p>
              {!infoError && (
                <button
                  type="button"
                  onClick={() => setIntroExpanded((v) => !v)}
                  className="mt-1 inline-flex items-center text-2xs text-brand-600"
                >
                  {introExpanded ? <>收起 <ChevronUp size={11} /></> : <>展开 <ChevronDown size={11} /></>}
                </button>
              )}
            </div>
          )}
        </div>
      </div>

      {/* 动作区 */}
      <div className="mt-6 flex gap-3">
        {shelfBook ? (
          <button type="button" onClick={removeFromShelf} className="btn-outline flex-1 text-xs">
            <Trash2 size={13} className="mr-1 inline" /> 移出书架
          </button>
        ) : (
          <button type="button" onClick={startReading} disabled={busy} className="btn-outline flex-1 text-xs disabled:opacity-50">
            <BookOpen size={13} className="mr-1 inline" /> 加入书架
          </button>
        )}
        <button type="button" onClick={startReading} disabled={busy || !firstReadableChapter} className="btn-primary flex-1 text-xs disabled:opacity-50">
          {busy ? <Loader2 size={13} className="mr-1 inline animate-spin" /> : null}
          {shelfBook?.dur_chapter_title ? '继续阅读' : '开始阅读'}
        </button>
      </div>

      {/* 目录 */}
      <div className="mt-8">
        <div className="flex items-center justify-between border-b border-[var(--app-border)] pb-2">
          <p className="text-sm font-bold text-[var(--app-text)]">
            目录 {chapters !== null && <span className="text-xs font-normal text-[var(--app-muted)]">（{chapters.length} 章）</span>}
          </p>
          {chapters !== null && chapters.length > 20 && (
            <button
              type="button"
              onClick={() => setTocExpanded((v) => !v)}
              className="text-xs text-brand-600"
            >
              {tocExpanded ? '收起' : '展开全部'}
            </button>
          )}
        </div>
        {chapters === null ? (
          <div className="flex justify-center py-10 text-[var(--app-muted)]">
            <Loader2 className="animate-spin" size={20} />
          </div>
        ) : (
          <div className={`mt-2 overflow-y-auto ${tocExpanded ? 'max-h-[70vh]' : 'max-h-80'}`}>
            {chapters.map((c) => (
              <div key={c.index} className="px-1 py-1.5 text-xs text-[var(--app-muted)]">
                {c.is_volume ? (
                  <p className="font-bold text-[var(--app-text)]">{c.title}</p>
                ) : (
                  c.title
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
