import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { AlertTriangle, ChevronLeft, ChevronRight, ImageOff, Search, WifiOff } from 'lucide-react'

import { qualityAPI, type MediaQualityIssue, type MediaQualityRow } from '../api/quality'

const tabs: { key: MediaQualityIssue; label: string; icon: typeof AlertTriangle }[] = [
  { key: 'unmatched', label: '未匹配元数据', icon: Search },
  { key: 'poster', label: '缺少海报', icon: ImageOff },
  { key: 'missing', label: '文件失联', icon: WifiOff },
]
const PAGE_SIZE = 50

export function MediaQualityPage() {
  const [params, setParams] = useSearchParams()
  const rawIssue = params.get('issue')
  const issue: MediaQualityIssue = rawIssue === 'poster' || rawIssue === 'missing' ? rawIssue : 'unmatched'
  const page = Math.max(1, Number.parseInt(params.get('page') ?? '1', 10) || 1)
  const [items, setItems] = useState<MediaQualityRow[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError('')
    void qualityAPI.list(issue, page, PAGE_SIZE).then((result) => {
      if (!cancelled) { setItems(result.items ?? []); setTotal(result.total ?? 0) }
    }).catch(() => {
      if (!cancelled) setError('读取质量检查结果失败，请重试')
    }).finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [issue, page, revision])

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const changePage = (nextPage: number) => setParams((current) => {
    const next = new URLSearchParams(current)
    if (nextPage <= 1) next.delete('page')
    else next.set('page', String(nextPage))
    return next
  })

  return (
    <div className="space-y-5">
      <header>
        <h1 className="font-display text-3xl font-bold text-ink-600">媒体质量检查</h1>
        <p className="mt-1 text-sm text-ink-50">集中查看待匹配、缺少海报和文件失联的媒体条目。</p>
      </header>
      <nav className="flex flex-wrap gap-2 border-b border-sand-200 pb-3">
        {tabs.map((tab) => {
          const Icon = tab.icon
          return <button key={tab.key} type="button" onClick={() => setParams(tab.key === 'unmatched' ? {} : { issue: tab.key })} className={`inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm font-semibold ${issue === tab.key ? 'bg-brand-500 text-white' : 'text-ink-100 hover:bg-brand-50'}`}><Icon size={15} />{tab.label}</button>
        })}
        <span className="ml-auto self-center text-sm text-ink-50">{loading ? '正在检查…' : error ? '读取失败' : `共 ${total} 项`}</span>
      </nav>
      {error && <div role="alert" className="glass-panel text-sm text-red-500">{error}<button type="button" className="ml-3 underline" onClick={() => setRevision((value) => value + 1)}>重试</button></div>}
      {!error && !loading && (items.length === 0 ? <div className="glass-panel py-12 text-center text-sm text-ink-50">{page > 1 ? '本页没有结果，请返回上一页。' : '当前分类没有待处理媒体。'}</div> : (
        <div className="divide-y divide-sand-200 overflow-hidden rounded-2xl border border-sand-200 bg-white/60">
          {items.map(({ media }) => <article key={media.id} className="flex flex-wrap items-center justify-between gap-3 p-4">
            <div className="min-w-0"><Link to={`/media/${media.id}`} className="break-words font-semibold text-ink-600 hover:text-brand-500">{media.title}</Link><p className="mt-1 break-all text-xs text-ink-50">{media.library_name || '未知媒体库'} · {media.path}</p><p className="mt-1 text-xs text-ink-50">{media.year || '年份未知'}{media.scrape_status ? ` · ${media.scrape_status}` : ''}{media.missing_since ? ` · 失联于 ${new Date(media.missing_since).toLocaleString()}` : ''}</p></div>
            <Link to={`/media/${media.id}`} className="btn-outline">查看并处理</Link>
          </article>)}
        </div>
      ))}
      {!loading && !error && (totalPages > 1 || page > 1) && <nav aria-label="媒体质量检查分页" className="flex items-center justify-center gap-4 py-3 text-sm">
        <button type="button" className="btn-outline" disabled={page <= 1} onClick={() => changePage(page - 1)}><ChevronLeft size={15} />上一页</button>
        <span className="text-ink-50">第 {page} / {totalPages} 页</span>
        <button type="button" className="btn-outline" disabled={page >= totalPages} onClick={() => changePage(page + 1)}>下一页<ChevronRight size={15} /></button>
      </nav>}
    </div>
  )
}
