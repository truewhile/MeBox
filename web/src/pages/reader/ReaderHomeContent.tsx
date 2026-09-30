import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { BookOpen, Loader2, RefreshCw, Search, Settings2 } from 'lucide-react'

import { readerAPI, type ReaderBook } from '../../api/reader'
import { ReaderModeSwitch } from './ReaderModeSwitch'

// 首页阅读模式的书架内容（首页切换与 /reader 路由共用）。
// 结构仿 legado 书架：网格封面 + 书名 + 阅读进度，右上搜索/书源管理入口。

export function ReaderHomeContent({ embedded = false }: { embedded?: boolean }) {
  const navigate = useNavigate()
  const [books, setBooks] = useState<ReaderBook[] | null>(null)
  const [error, setError] = useState('')

  const load = () => {
    setError('')
    readerAPI
      .listBooks()
      .then(setBooks)
      .catch((e) => setError(e?.response?.data?.error ?? '加载书架失败'))
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, [])

  return (
    <div className="space-y-6">
      {!embedded && <ReaderModeSwitch />}

      <div className="flex items-center justify-between gap-3">
        <h1 className="font-display text-2xl text-ink-600">书架</h1>
        <div className="flex items-center gap-2">
          <Link
            to="/reader/sources"
            className="flex items-center gap-1.5 rounded-xl border border-[var(--app-border)] px-3 py-1.5 text-xs font-bold text-[var(--app-muted)] hover:text-[var(--app-text)]"
          >
            <Settings2 size={13} /> 书源管理
          </Link>
          <Link
            to="/reader/search"
            className="flex items-center gap-1.5 rounded-xl border border-brand-500/60 bg-brand-500/10 px-3 py-1.5 text-xs font-bold text-brand-600 hover:bg-brand-500/20"
          >
            <Search size={13} /> 搜索
          </Link>
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
          <p className="mt-1 text-xs text-[var(--app-muted)]">先到「书源管理」导入书源，再搜索加入书架</p>
          <div className="mt-5 flex items-center justify-center gap-3">
            <Link to="/reader/sources" className="btn-outline text-xs">导入书源</Link>
            <Link to="/reader/search" className="btn-primary text-xs">去搜索</Link>
          </div>
        </div>
      )}

      {books !== null && books.length > 0 && (
        <div className="grid grid-cols-3 gap-x-4 gap-y-6 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-8">
          {books.map((book) => (
            <button
              key={book.id}
              type="button"
              onClick={() => navigate(`/reader/view/${book.id}`)}
              className="group text-left"
            >
              <div className="relative overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] shadow-sm transition group-hover:shadow-md">
                <div className="aspect-[3/4] w-full">
                  {book.cover_url ? (
                    <img
                      src={book.cover_url}
                      alt={book.name}
                      loading="lazy"
                      referrerPolicy="no-referrer"
                      className="h-full w-full object-cover"
                    />
                  ) : (
                    <div className="flex h-full w-full items-center justify-center">
                      <BookOpen size={22} className="text-[var(--app-muted)]" />
                    </div>
                  )}
                </div>
              </div>
              <p className="mt-2 truncate text-xs font-bold text-[var(--app-text)]">{book.name}</p>
              <p className="truncate text-2xs text-[var(--app-muted)]">
                {book.dur_chapter_title ? `读到 ${book.dur_chapter_title}` : book.author || '未开始阅读'}
              </p>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
