import { useEffect, useState } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import { ChevronLeft, ChevronRight, LoaderCircle, RefreshCw, Search } from 'lucide-react'

import { mediaAPI, type MediaSearchPage as SearchPageData } from '../api/library'
import { libraryAPI } from '../api/library'
import { MediaCard } from '../components/MediaCard'
import type { Library } from '../types'
import { PageBackButton } from '../components/PageBackButton'
import { normalizeListPosition } from '../hooks/useListPositionMemory'
import { useAuthStore } from '../stores/auth'
import { usePlayProfileStore } from '../stores/playProfile'
import { favouriteMediaLink } from '../utils/mediaNavigation'

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
  const filters = {
    libraryId: params.get('library_id') ?? '',
    yearMin: params.get('year_min') ?? '',
    yearMax: params.get('year_max') ?? '',
    genres: params.get('genre') ?? '',
    sort: params.get('sort') ?? '',
  }
  const userId = useAuthStore((s) => s.user?.id)
  const profileId = usePlayProfileStore((s) => s.activeProfileId)
  const cacheKey = JSON.stringify([userId, profileId, query, page, filters])

  const changePage = (next: number) => {
    const nextParams = new URLSearchParams(params)
    nextParams.set('q', query)
    if (next > 1) nextParams.set('page', String(next))
    else nextParams.delete('page')
    setParams(nextParams)
  }

  const changeFilter = (key: keyof typeof filters, value: string) => {
    const next = new URLSearchParams(params)
    const queryKey = { libraryId: 'library_id', yearMin: 'year_min', yearMax: 'year_max', genres: 'genre', sort: 'sort' }[key]
    if (value.trim()) next.set(queryKey, value)
    else next.delete(queryKey)
    next.delete('page')
    setParams(next, { replace: true })
  }

  return (
    <MediaSearchResults
      key={JSON.stringify([userId, profileId])}
      cacheKey={cacheKey}
      query={query}
      page={page}
      onPageChange={changePage}
      filters={filters}
      onFilterChange={changeFilter}
    />
  )
}

function MediaSearchResults({ cacheKey, query, page, onPageChange, filters, onFilterChange }: {
  cacheKey: string
  query: string
  page: number
  onPageChange: (page: number) => void
  filters: { libraryId: string; yearMin: string; yearMax: string; genres: string; sort: string }
  onFilterChange: (key: keyof { libraryId: string; yearMin: string; yearMax: string; genres: string; sort: string }, value: string) => void
}) {
  const location = useLocation()
  const [data, setData] = useState(() => readCachedPage(cacheKey))
  const [loading, setLoading] = useState(() => !!query && !readCachedPage(cacheKey))
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const [libraries, setLibraries] = useState<Library[]>([])

  useEffect(() => { void libraryAPI.list().then(setLibraries).catch(() => setLibraries([])) }, [])

  useEffect(() => {
    if (!query) {
      setData(null)
      setLoading(false)
      setError('')
      return
    }
    setError('')
    const cached = readCachedPage(cacheKey)
    if (cached) {
      setData(cached)
      setLoading(false)
      return
    }

    let cancelled = false
    setLoading(true)
    setData(null)
    mediaAPI.searchPage(query, page, PAGE_SIZE, {
      groupSeries: true,
      libraryId: filters.libraryId || undefined,
      yearMin: Number(filters.yearMin) || undefined,
      yearMax: Number(filters.yearMax) || undefined,
      genres: filters.genres.split(/[,，]/).map((value) => value.trim()).filter(Boolean),
      sort: filters.sort === 'title_asc' || filters.sort === 'year_desc' || filters.sort === 'rating_desc' ? filters.sort : undefined,
    })
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
  }, [cacheKey, query, page, revision, filters.libraryId, filters.yearMin, filters.yearMax, filters.genres, filters.sort])

  const refresh = () => {
    pageCache.delete(cacheKey)
    setRevision((value) => value + 1)
  }

  const items = data?.items ?? []
  const total = data?.total ?? items.length
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const from = location.pathname + location.search

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

      {query && (
        <div className="grid gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4 sm:grid-cols-2 lg:grid-cols-5">
          <label className="space-y-1 text-xs text-[var(--app-muted)]">媒体库
            <select value={filters.libraryId} onChange={(event) => onFilterChange('libraryId', event.target.value)} className="block w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-sm text-[var(--app-text)]">
              <option value="">所有媒体库</option>{libraries.map((library) => <option key={library.id} value={library.id}>{library.name}</option>)}
            </select>
          </label>
          <label className="space-y-1 text-xs text-[var(--app-muted)]">年份从
            <input type="number" min={1870} max={2200} value={filters.yearMin} onChange={(event) => onFilterChange('yearMin', event.target.value)} placeholder="不限" className="block w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-sm text-[var(--app-text)]" />
          </label>
          <label className="space-y-1 text-xs text-[var(--app-muted)]">年份至
            <input type="number" min={1870} max={2200} value={filters.yearMax} onChange={(event) => onFilterChange('yearMax', event.target.value)} placeholder="不限" className="block w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-sm text-[var(--app-text)]" />
          </label>
          <label className="space-y-1 text-xs text-[var(--app-muted)]">类型（逗号分隔）
            <input value={filters.genres} onChange={(event) => onFilterChange('genres', event.target.value)} placeholder="动画,科幻" className="block w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-sm text-[var(--app-text)]" />
          </label>
          <label className="space-y-1 text-xs text-[var(--app-muted)]">排序
            <select value={filters.sort} onChange={(event) => onFilterChange('sort', event.target.value)} className="block w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-3 py-2 text-sm text-[var(--app-text)]">
              <option value="">相关度 / 最近添加</option><option value="title_asc">名称 A–Z</option><option value="year_desc">年份从新到旧</option><option value="rating_desc">评分从高到低</option>
            </select>
          </label>
        </div>
      )}

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

      {!loading && !error && query && items.length > 0 && (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
          {items.map((item) => (
            <MediaCard key={item.id} media={item} showLibrary linkTo={favouriteMediaLink(item)} linkState={{ from }} />
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
