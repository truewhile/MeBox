import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Download, ListChecks, Sparkles, Upload } from 'lucide-react'
import clsx from 'clsx'

import { scraperAPI } from '../api/scraper'
import { strmAPI } from '../api/strm'
import { ScraperQueuePage } from './ScraperQueuePage'
import { StrmQueuePanel } from './StrmQueuePage'
import { BackgroundTasksPanel } from './BackgroundTasksPanel'

type QueueType = 'all' | 'scrape' | 'download' | 'upload'

const QUEUE_TABS: { type: QueueType; label: string; icon: typeof ListChecks }[] = [
  { type: 'all', label: '全部', icon: ListChecks },
  { type: 'scrape', label: '刮削队列', icon: Sparkles },
  { type: 'download', label: '下载队列', icon: Download },
  { type: 'upload', label: '上传队列', icon: Upload },
]

function normalizeType(raw: string | null): QueueType {
  if (raw === 'scrape' || raw === 'download' || raw === 'upload' || raw === 'all') {
    return raw
  }
  return 'all'
}

/**
 * 任务队列：整合刮削 / 下载 / 上传 三类队列的统一入口，按类型 Tab 区分。
 * 单个类型视图直接复用原队列组件；「全部」纵向堆叠三个队列。
 */
export function TaskQueuePage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const type = normalizeType(searchParams.get('type'))

  const [scrapeCounts, setScrapeCounts] = useState<Record<string, number> | null>(null)
  const [downloadCounts, setDownloadCounts] = useState<Record<string, number> | null>(null) // StrmQueueCounts
  const [uploadCounts, setUploadCounts] = useState<Record<string, number> | null>(null)

  const loadOverview = useCallback(async () => {
    try {
      const scrape = await scraperAPI.queue(undefined, 1, 1)
      setScrapeCounts(scrape.counts as unknown as Record<string, number>)
    } catch {
      setScrapeCounts(null)
    }
    try {
      const dl = await strmAPI.downloads(undefined, 1, 1)
      setDownloadCounts(dl.counts as unknown as Record<string, number>)
    } catch {
      setDownloadCounts(null)
    }
    try {
      const ul = await strmAPI.uploads(undefined, 1, 1)
      setUploadCounts(ul.counts as unknown as Record<string, number>)
    } catch {
      setUploadCounts(null)
    }
  }, [])

  useEffect(() => {
    loadOverview()
  }, [loadOverview])

  const countBadge = (counts: Record<string, number> | null) => {
    if (!counts) return null
    const total = Object.values(counts).reduce((a, b) => a + (b || 0), 0)
    return <span className="tab-count">{total}</span>
  }

  const switchType = (next: QueueType) => {
    setSearchParams(next === 'all' ? {} : { type: next }, { replace: true })
  }

  return (
    <div className="space-y-5">
      <BackgroundTasksPanel />
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-bold text-ink-600">任务队列</h1>
          <p className="mt-0.5 text-sm text-ink-50">刮削 / 下载 / 上传三类任务统一管理，按类型切换查看</p>
        </div>
        <button onClick={loadOverview} className="btn-outline" title="刷新概览计数">
          <ListChecks size={14} />
          刷新计数
        </button>
      </div>

      {/* 类型 Tab */}
      <div className="flex flex-wrap gap-2 border-b border-sand-200 pb-3">
        {QUEUE_TABS.map((tab) => {
          const active = type === tab.type
          const Icon = tab.icon
          const badge =
            tab.type === 'scrape'
              ? countBadge(scrapeCounts)
              : tab.type === 'download'
                ? countBadge(downloadCounts)
                : tab.type === 'upload'
                  ? countBadge(uploadCounts)
                  : null
          return (
            <button
              key={tab.type}
              onClick={() => switchType(tab.type)}
              className={clsx(
                'inline-flex items-center gap-1.5 rounded-full px-4 py-2 text-sm font-semibold transition',
                active ? 'bg-brand-500 text-white shadow-sm' : 'text-ink-100 hover:bg-brand-50 hover:text-brand-600',
              )}
            >
              <Icon size={14} />
              {tab.label}
              {badge}
            </button>
          )
        })}
      </div>

      {/* 内容 */}
      {type === 'scrape' && <ScraperQueuePage embedded />}
      {type === 'download' && <StrmQueuePanel kind="download" embedded />}
      {type === 'upload' && <StrmQueuePanel kind="upload" embedded />}
      {type === 'all' && (
        <div className="space-y-10">
          <section>
            <h2 className="mb-2 flex items-center gap-2 font-display text-xl font-bold text-ink-600">
              <Sparkles size={18} className="text-brand-500" />
              刮削队列
            </h2>
            <ScraperQueuePage embedded />
          </section>
          <section>
            <h2 className="mb-2 flex items-center gap-2 font-display text-xl font-bold text-ink-600">
              <Download size={18} className="text-brand-500" />
              下载队列
            </h2>
            <StrmQueuePanel kind="download" embedded />
          </section>
          <section>
            <h2 className="mb-2 flex items-center gap-2 font-display text-xl font-bold text-ink-600">
              <Upload size={18} className="text-brand-500" />
              上传队列
            </h2>
            <StrmQueuePanel kind="upload" embedded />
          </section>
        </div>
      )}
    </div>
  )
}
