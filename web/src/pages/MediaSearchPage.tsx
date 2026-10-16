import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ChevronLeft, ChevronRight, LoaderCircle, RefreshCw, Search } from 'lucide-react'

import { mediaAPI, type MediaSearchPage as SearchPageData } from '../api/library'
import { MediaCard } from '../components/MediaCard'
import { PageBackButton } from '../components/PageBackButton'
import { normalizeListPosition } from '../hooks/useListPositionMemory'
import { useAuthStore } from '../stores/auth'
import { usePlayProfileStore } from '../stores/playProfile'
import { favouriteMediaLink, mediaSearchLink } from '../utils/mediaNavigation'

const PAGE_SIZE = 48
const CACHE_TTL_MS = 5 * 60_000
// 返回详情前的结果可立即显示；按账号、播放档案、关键词和页码隔离。
const pageCache = new Map<string, { at: number; data: SearchPageData }>()

function readCachedPage(key: string): SearchPageData | null {
  const cached = pageCache.get(key)
  if (!cached) return null
  if (Date.now() - cached.at >= CACHE_TTL_MS) {
    pageCache.delete(key)
    return null
  }
  return cached.data
}

export function MediaSearchPage() {
  const [params, setParams] = useSearchParams()
  const query = (params.get('q') ?? '').trim()
  const page = normalizeListPosition(params.get('page'))
  const userId = useAuthStore((s) => s.user?.id)
  const profileId = usePlayProfileStore((s) => s.activeProfileId)
  const cacheKey = JSON.stringify([userId, profileId, query, page])

  const changePage = (next: number) => {
    const nextParams = new URLSearchParams({ q: query })
    if (next > 1) nextParams.set('page', String(next))
    setParams(nextParams)
  }

  return (
    <MediaSearchResults
      key={cacheKey}
      cacheKey={cacheKey}
      query={query}
      page={page}
      onPageChange={changePage}
    />
  )
}

function MediaSearchResults({ cacheKey, query, page, onPageChange }: {
  cacheKey: string
  query: string
  page: number
  onPageChange: (page: number) => void
}) {
  const [data, setData] = useState(() => readCachedPage(cacheKey))
  const [loading, setLoading] = useState(() => !!query && !readCachedPage(cacheKey))
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    if (!query) return
    const cached = readCachedPage(cacheKey)
    if (cached) {
      setData(cached)
      setLoading(false)
      return
    }

    let cancelled = false
    setLoading(true)
    setError('')
    mediaAPI.searchPage(query, page, PAGE_SIZE, { groupSeries: true })
      .then((result) => {
        if (cancelled) return
        // 限制缓存大小，避免长时间搜索大量关键词后占用过多内存。
        if (pageCache.size >= 20) pageCache.delete(pageCache.keys().next().value!)
        pageCache.set(cacheKey, { at: Date.now(), data: result })
        setData(result)
      })
      .catch(() => {
        if (!cancelled) setError('搜索失败，请稍后重试')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => { cancelled = true }
  }, [cacheKey, query, page, revision])

  const refresh = () => {
    pageCache.delete(cacheKey)
    setRevision((value) => value + 1)
  }

  const items = data?.items ?? []
  const total = data?.total ?? items.length
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const from = mediaSearchLink(query, page)

  return (
    <div className="space-y-6">
      <PageBackButton to="/" label="返回首页" compact />
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 space-y-2">
          <h1 className="font-display text-2xl font-bold text-[var(--app-text)] sm:text-3xl">搜索结果</h1>
          <p className="break-words text-sm text-[var(--app-muted)]" aria-live="polite">
            {query
              ? <>“{query}”{data && !loading ? ` · 共 ${total} 条结果` : ''}</>
              : '在顶部搜索框输入媒体名称，按回车开始搜索'}
          </p>
        </div>
        {query && (
          <button
            type="button"
            onClick={refresh}
            disabled={loading}
            className="inline-flex shrink-0 items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] px-3 py-2 text-xs text-[var(--app-subtle)] hover:bg-[var(--app-hover)] disabled:opacity-50"
          >
            <RefreshCw size={14} />
            刷新结果
          </button>
        )}
      </div>

      {loading && (
        <div role="status" className="flex items-center justify-center gap-2 py-20 text-sm text-[var(--app-muted)]">
          <LoaderCircle size={18} className="animate-spin text-brand-500" />
          正在搜索…
        </div>
      )}

      {error && (
        <div role="alert" className="rounded-2xl border border-red-400/30 bg-[var(--app-panel)] p-5 text-sm text-red-400">
          {error}
          <button type="button" onClick={refresh} className="ml-3 underline">重试</button>
        </div>
      )}

      {!loading && !error && query && items.length === 0 && (
        <div className="flex flex-col items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] px-6 py-16 text-center text-[var(--app-muted)]">
          <Search size={28} />
          <p className="break-all text-sm">未搜索到与 “{query}” 相关的媒体内容</p>
          <p className="text-xs">试试其他名称或更简短的关键词</p>
        </div>
      )}

      {!loading && items.length > 0 && (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
          {items.map((item) => (
            <MediaCard key={item.id} media={item} linkTo={favouriteMediaLink(item)} linkState={{ from }} />
          ))}
        </div>
      )}

      {!loading && !error && query && (totalPages > 1 || page > 1) && (
        <nav aria-label="搜索结果分页" className="flex items-center justify-center gap-4 py-4 text-sm">
          <button
            type="button"
            disabled={page <= 1}
            onClick={() => onPageChange(page - 1)}
            className="inline-flex items-center gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] px-3 py-2 hover:bg-[var(--app-hover)] disabled:cursor-not-allowed disabled:opacity-40"
          >
            <ChevronLeft size={16} />上一页
          </button>
          <span className="text-[var(--app-muted)]">第 {page} / {totalPages} 页</span>
          <button
            type="button"
            disabled={page >= totalPages}
            onClick={() => onPageChange(page + 1)}
            className="inline-flex items-center gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] px-3 py-2 hover:bg-[var(--app-hover)] disabled:cursor-not-allowed disabled:opacity-40"
          >
            下一页<ChevronRight size={16} />
          </button>
        </nav>
      )}
    </div>
  )
}
