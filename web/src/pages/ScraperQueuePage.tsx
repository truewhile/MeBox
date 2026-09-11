import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import toast from 'react-hot-toast'
import {
  AlertCircle,
  Ban,
  CheckCircle2,
  Clock,
  Eye,
  Film,
  Image as ImageIcon,
  Layers,
  Loader2,
  PlayCircle,
  RefreshCw,
  Search,
  Sparkles,
  Trash2,
  Tv,
  X,
} from 'lucide-react'

import { imageURL } from '../api/client'
import { scraperAPI } from '../api/scraper'
import type { ScrapeQueueSnapshot, ScrapeTask, ScrapeTaskStatus } from '../types/scraper'
import { apiErrorMessage, formatTime, taskStatusMeta } from './StrmManagePage'
import { ScrapeDetailModal } from './scraper-queue/ScrapeDetailModal'
import { PROVIDER_LABELS } from './scraper-queue/scrapeLabels'
import { copyToClipboard, useTaskSelection } from './queue-shared'

const FILTERS: { key: 'all' | ScrapeTaskStatus; label: string; icon: typeof Clock; color: string }[] = [
  { key: 'all', label: '全部', icon: Sparkles, color: 'text-ink-600' },
  { key: 'pending', label: '排队中', icon: Clock, color: 'text-gray-500' },
  { key: 'running', label: '刮削中', icon: PlayCircle, color: 'text-brand-500' },
  { key: 'done', label: '已匹配', icon: CheckCircle2, color: 'text-emerald-500' },
  { key: 'failed', label: '未匹配/失败', icon: AlertCircle, color: 'text-rose-500' },
  { key: 'canceled', label: '已取消', icon: Ban, color: 'text-amber-500' },
]

const TYPE_ICONS: Record<string, ReactNode> = {
  movie: <Film size={14} className="text-blue-500" />,
  tv: <Tv size={14} className="text-purple-500" />,
  anime: <Layers size={14} className="text-emerald-500" />,
  adult: <Film size={14} className="text-rose-500" />,
}

const PAGE_SIZE = 50

export function ScraperQueuePage({ embedded = false }: { embedded?: boolean }) {
  const [snapshot, setSnapshot] = useState<ScrapeQueueSnapshot | null>(null)
  const [filter, setFilter] = useState<'all' | ScrapeTaskStatus>('all')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)
  const [isRefreshing, setIsRefreshing] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [batchBusy, setBatchBusy] = useState(false)
  const { selectedIds, setSelectedIds, reset: clearSelection, toggleRow: toggleSelectRow, toggleAll: toggleAllIds } = useTaskSelection()
  const [detailTask, setDetailTask] = useState<ScrapeTask | null>(null)
  // 轮询/翻页/切筛选并发时用递增序号丢弃过期响应
  const refreshSeqRef = useRef(0)
  const [pollFailures, setPollFailures] = useState(0)

  const refresh = useCallback(
    async (showLoading = false) => {
      if (showLoading) setIsRefreshing(true)
      const seq = ++refreshSeqRef.current
      try {
        const status = filter === 'all' ? undefined : filter
        const data = await scraperAPI.queue(status, page, PAGE_SIZE)
        // 序号不符说明已有更新的请求发出（翻页/切筛选/轮询并发），丢弃旧响应
        if (seq !== refreshSeqRef.current) return
        setPollFailures(0)
        const tp = Math.max(1, Math.ceil((data.total ?? data.tasks.length) / PAGE_SIZE))
        if (page > tp) {
          setPage(tp)
          return
        }
        setTotalPages(tp)
        setSnapshot(data)
      } catch {
        // 保留旧数据；连续失败 ≥3 次时页头徽标切换为「连接失败，重试中」
        if (seq === refreshSeqRef.current) setPollFailures((n) => n + 1)
      } finally {
        setLoading(false)
        if (showLoading) setIsRefreshing(false)
      }
    },
    [filter, page],
  )

  useEffect(() => {
    refresh().catch(() => undefined)
  }, [refresh])

  useEffect(() => {
    if (!autoRefresh) return
    const timer = setInterval(() => {
      if (document.hidden) return
      refresh().catch(() => undefined)
    }, 3000)
    return () => clearInterval(timer)
  }, [autoRefresh, refresh])

  useEffect(() => {
    clearSelection()
  }, [filter, page, clearSelection])

  const copyText = copyToClipboard

  // Row actions
  const cancelTask = async (task: ScrapeTask) => {
    try {
      await scraperAPI.cancelTask(task.id)
      toast.success('已取消刮削任务')
      await refresh()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  const retryTask = async (task: ScrapeTask) => {
    try {
      await scraperAPI.retryTask(task.id)
      toast.success('已重新推入刮削队列')
      await refresh()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  const deleteTask = async (task: ScrapeTask) => {
    try {
      await scraperAPI.deleteTask(task.id)
      toast.success('已删除刮削记录')
      if (detailTask?.id === task.id) setDetailTask(null)
      await refresh()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }

  // Batch actions
  const runSelectedBatch = async (action: 'retry' | 'cancel' | 'delete') => {
    const ids = Array.from(selectedIds)
    if (ids.length === 0) return

    const actionText = action === 'retry' ? '重新刮削' : action === 'cancel' ? '取消' : '删除'
    if (action === 'delete' && !window.confirm(`确定删除选中的 ${ids.length} 条刮削记录？`)) return
    if (action === 'cancel' && !window.confirm(`确定取消选中的 ${ids.length} 个刮削任务？`)) return

    setBatchBusy(true)
    try {
      const res = await scraperAPI.batchAction(action, ids)
      toast.success(`已成功${actionText} ${res.affected} 项`)
      setSelectedIds(new Set())
      await refresh()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setBatchBusy(false)
    }
  }

  const runGlobalBatch = async (
    action: () => Promise<{ deleted?: number; retried?: number; canceled?: number }>,
    confirmMsg?: string,
  ) => {
    if (confirmMsg && !window.confirm(confirmMsg)) return
    setBatchBusy(true)
    try {
      const res = await action()
      if (res.deleted !== undefined) toast.success(`已清空 ${res.deleted} 条记录`)
      else if (res.retried !== undefined) toast.success(`已重新入队 ${res.retried} 个任务`)
      else if (res.canceled !== undefined) toast.success(`已取消 ${res.canceled} 个任务`)
      await refresh()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setBatchBusy(false)
    }
  }

  // Enqueue all libraries
  const handleEnqueueAll = async () => {
    if (!window.confirm('确定将全库所有未匹配或需要更新的媒体重新推入刮削队列？')) return
    setBatchBusy(true)
    try {
      const res = await scraperAPI.enqueueAll({ include_matched: false, refresh_matched: false, episode_images: true })
      toast.success(`已将 ${res.enqueued} 个媒体项推入刮削队列`)
      await refresh()
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setBatchBusy(false)
    }
  }

  // Filter and search
  const filteredTasks = useMemo(() => {
    let list = snapshot?.tasks ?? []
    if (filter !== 'all') {
      list = list.filter((t) => t.status === filter)
    }
    if (search.trim()) {
      const q = search.trim().toLowerCase()
      list = list.filter(
        (t) =>
          t.media_title.toLowerCase().includes(q) ||
          t.matched_title.toLowerCase().includes(q) ||
          t.library_name.toLowerCase().includes(q) ||
          t.media_path.toLowerCase().includes(q) ||
          (t.error && t.error.toLowerCase().includes(q)),
      )
    }
    return list
  }, [snapshot?.tasks, filter, search])

  const counts = snapshot?.counts
  const pendingCount = counts?.pending ?? 0
  const runningCount = counts?.running ?? 0
  const activeTaskCount = pendingCount + runningCount
  const doneCount = counts?.done ?? 0
  const failedCount = counts?.failed ?? 0
  const canceledCount = counts?.canceled ?? 0
  const finishedCount = doneCount + failedCount + canceledCount
  const allCurrentChecked =
    filteredTasks.length > 0 && filteredTasks.every((t) => selectedIds.has(t.id))

  const toggleSelectAll = () => toggleAllIds(filteredTasks.map((t) => t.id))

  return (
    <div className="space-y-6">
      {!embedded && (
      <header className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3">
          <div className="flex h-11 w-11 items-center justify-center rounded-2xl border border-primary-400/30 bg-primary-400/10 text-brand-500 shadow-sm">
            <Sparkles size={22} />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h1 className="font-display text-2xl font-bold text-ink-600 sm:text-3xl">刮削队列</h1>
              {autoRefresh &&
                (pollFailures >= 3 ? (
                  <span className="inline-flex items-center gap-1 rounded-full border border-rose-300/40 bg-rose-500/10 px-2 py-0.5 text-[11px] font-semibold text-rose-600">
                    <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-rose-500" />
                    连接失败，重试中
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1 rounded-full border border-emerald-300/40 bg-emerald-500/10 px-2 py-0.5 text-[11px] font-semibold text-emerald-600">
                    <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500" />
                    实时同步
                  </span>
                ))}
            </div>
            <p className="text-xs text-sand-500 mt-0.5">
              媒体元数据在线识别与海报/剧照下载进度（TMDb / 豆瓣 / Bangumi / TheTVDB）
            </p>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={batchBusy}
            onClick={handleEnqueueAll}
            className="inline-flex items-center gap-1.5 rounded-xl border border-brand-500/40 bg-brand-500/10 px-3 py-2 text-xs font-semibold text-brand-500 shadow-sm transition hover:bg-brand-500/20 disabled:opacity-50"
            title="将所有媒体库未刮削媒体加入队列"
          >
            <Sparkles size={13} />
            <span>全库重新刮削</span>
          </button>

          <button
            type="button"
            onClick={() => setAutoRefresh((v) => !v)}
            className={`inline-flex items-center gap-1.5 rounded-xl border px-3 py-2 text-xs font-semibold transition ${
              autoRefresh
                ? 'border-emerald-300/50 bg-emerald-50 text-emerald-700 hover:bg-emerald-100/70'
                : 'border-gray-200 bg-white text-ink-50 hover:bg-gray-50'
            }`}
            title={autoRefresh ? '点击暂停自动刷新' : '点击开启 3 秒自动轮询'}
          >
            <Clock size={13} />
            <span>自动刷新: {autoRefresh ? '开启' : '已暂停'}</span>
          </button>

          <button
            type="button"
            disabled={isRefreshing}
            onClick={() => refresh(true)}
            className="inline-flex items-center gap-1.5 rounded-xl border border-gray-200 bg-white px-3 py-2 text-xs font-semibold text-ink-100 shadow-sm transition hover:border-gray-300 hover:bg-gray-50"
            title="手动刷新"
          >
            <RefreshCw size={13} className={isRefreshing ? 'animate-spin text-brand-500' : ''} />
            <span>刷新</span>
          </button>

          <details className="relative inline-block">
            <summary className="inline-flex cursor-pointer list-none items-center gap-1.5 rounded-xl border border-gray-200 bg-white px-3 py-2 text-xs font-semibold text-ink-100 shadow-sm transition hover:border-gray-300 hover:bg-gray-50 [&::-webkit-details-marker]:hidden">
              <Trash2 size={13} className="text-sand-500" />
              <span>批量操作</span>
            </summary>
            <div className="absolute right-0 top-10 z-30 min-w-44 rounded-xl border border-gray-200 bg-white p-1.5 shadow-xl backdrop-blur">
              {failedCount > 0 && (
                <button
                  type="button"
                  disabled={batchBusy}
                  onClick={(e) => {
                    e.currentTarget.closest('details')?.removeAttribute('open')
                    runGlobalBatch(() => scraperAPI.retryFailed(), '确定重新入队所有失败任务？')
                  }}
                  className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-brand-500 hover:bg-brand-50"
                >
                  <RefreshCw size={13} />
                  <span>重试所有失败 ({failedCount})</span>
                </button>
              )}
              {activeTaskCount > 0 && (
                <button
                  type="button"
                  disabled={batchBusy}
                  onClick={(e) => {
                    e.currentTarget.closest('details')?.removeAttribute('open')
                    runGlobalBatch(() => scraperAPI.cancelPending(), '确定取消所有排队及进行中的刮削任务？')
                  }}
                  className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-amber-600 hover:bg-amber-50"
                >
                  <Ban size={13} />
                  <span>取消所有进行中 ({activeTaskCount})</span>
                </button>
              )}
              <div className="my-1 border-t border-gray-100" />
              <button
                type="button"
                disabled={batchBusy}
                onClick={(e) => {
                  e.currentTarget.closest('details')?.removeAttribute('open')
                  runGlobalBatch(() => scraperAPI.clearDone(), '确定清空所有已匹配完成的记录？')
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-ink-100 hover:bg-gray-50"
              >
                <CheckCircle2 size={13} className="text-emerald-500" />
                <span>清空已完成记录</span>
              </button>
              <button
                type="button"
                disabled={batchBusy}
                onClick={(e) => {
                  e.currentTarget.closest('details')?.removeAttribute('open')
                  runGlobalBatch(() => scraperAPI.clearCanceled(), '确定清空所有已取消的任务记录？')
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-ink-100 hover:bg-gray-50"
              >
                <Ban size={13} className="text-amber-500" />
                <span>清空已取消记录</span>
              </button>
              <button
                type="button"
                disabled={batchBusy}
                onClick={(e) => {
                  e.currentTarget.closest('details')?.removeAttribute('open')
                  runGlobalBatch(() => scraperAPI.clearFinished(), '确定清空所有已完成、失败及取消的历史记录？')
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-rose-500 hover:bg-rose-50"
              >
                <Trash2 size={13} />
                <span>清空全部历史记录</span>
              </button>
            </div>
          </details>
        </div>
      </header>
      )}

      {/* 2. Status Cards */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        {FILTERS.map((item) => {
          const count =
            item.key === 'all'
              ? (counts?.pending ?? 0) +
                (counts?.running ?? 0) +
                (counts?.done ?? 0) +
                (counts?.failed ?? 0) +
                (counts?.canceled ?? 0)
              : counts?.[item.key] ?? 0
          const isActive = filter === item.key
          const ItemIcon = item.icon

          return (
            <button
              key={item.key}
              type="button"
              onClick={() => {
                setFilter(item.key)
                setPage(1)
              }}
              className={`flex flex-col justify-between rounded-2xl border p-3.5 text-left transition-all duration-200 select-none ${
                isActive
                  ? 'border-brand-500 bg-primary-400/10 shadow-sm ring-2 ring-brand-500/20'
                  : 'border-gray-200 bg-white/80 hover:border-gray-300 hover:bg-white'
              }`}
            >
              <div className="flex items-center justify-between text-xs text-sand-500">
                <span className="font-semibold">{item.label}</span>
                <ItemIcon size={14} className={item.color} />
              </div>
              <div className="mt-2 flex items-baseline gap-1">
                <span className={`font-display text-2xl font-black ${item.color}`}>{count}</span>
                <span className="text-[10px] text-sand-400 font-medium">项</span>
              </div>
            </button>
          )
        })}
      </div>

      {/* 3. Search & Batch Actions */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative flex-1 max-w-md">
          <Search size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索媒体标题、匹配结果、媒体库或错误信息…"
            className="h-9 w-full rounded-xl border border-gray-200 bg-white pl-9 pr-8 text-xs text-ink-600 placeholder:text-gray-400 outline-none transition focus:border-brand-500 focus:ring-2 focus:ring-brand-100/60"
          />
          {search && (
            <button
              type="button"
              onClick={() => setSearch('')}
              className="absolute right-2.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-gray-400 hover:text-ink-600"
            >
              <X size={13} />
            </button>
          )}
        </div>

        {selectedIds.size > 0 ? (
          <div className="flex flex-wrap items-center gap-2 rounded-xl border border-brand-500/30 bg-primary-400/10 px-3 py-2 text-xs animate-in fade-in zoom-in-95">
            <span className="font-bold text-brand-500">已选中 {selectedIds.size} 项</span>
            <div className="h-3.5 w-px bg-brand-300/40 mx-1" />
            <button
              type="button"
              disabled={batchBusy}
              onClick={() => runSelectedBatch('retry')}
              className="inline-flex items-center gap-1 rounded-lg border border-brand-500/40 bg-white px-2 py-1 font-semibold text-brand-500 hover:bg-brand-50 disabled:opacity-50"
            >
              <RefreshCw size={12} />
              重试选中
            </button>
            <button
              type="button"
              disabled={batchBusy}
              onClick={() => runSelectedBatch('cancel')}
              className="inline-flex items-center gap-1 rounded-lg border border-amber-300 bg-white px-2 py-1 font-semibold text-amber-600 hover:bg-amber-50 disabled:opacity-50"
            >
              <Ban size={12} />
              取消选中
            </button>
            <button
              type="button"
              disabled={batchBusy}
              onClick={() => runSelectedBatch('delete')}
              className="inline-flex items-center gap-1 rounded-lg border border-rose-300 bg-white px-2 py-1 font-semibold text-rose-600 hover:bg-rose-50 disabled:opacity-50"
            >
              <Trash2 size={12} />
              删除选中
            </button>
            <button
              type="button"
              onClick={() => setSelectedIds(new Set())}
              className="p-1 text-gray-400 hover:text-ink-600"
              title="清空选择"
            >
              <X size={13} />
            </button>
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-2">
            {/* 当前状态专属快捷批量按钮 */}
            {filter === 'all' && (
              <>
                {failedCount > 0 && (
                  <button
                    type="button"
                    disabled={batchBusy}
                    onClick={() => runGlobalBatch(() => scraperAPI.retryFailed(), '确定重新入队所有失败任务？')}
                    className="inline-flex items-center gap-1 rounded-xl border border-brand-500/40 bg-white px-3 py-1.5 text-xs font-semibold text-brand-500 hover:bg-brand-50 disabled:opacity-50"
                  >
                    <RefreshCw size={12} />
                    全部重试 ({failedCount})
                  </button>
                )}
                {activeTaskCount > 0 && (
                  <button
                    type="button"
                    disabled={batchBusy}
                    onClick={() => runGlobalBatch(() => scraperAPI.cancelPending(), '确定取消所有排队及进行中的刮削任务？')}
                    className="inline-flex items-center gap-1 rounded-xl border border-amber-300 bg-white px-3 py-1.5 text-xs font-semibold text-amber-600 hover:bg-amber-50 disabled:opacity-50"
                  >
                    <Ban size={12} />
                    全部取消 ({activeTaskCount})
                  </button>
                )}
                {finishedCount > 0 && (
                  <button
                    type="button"
                    disabled={batchBusy}
                    onClick={() => runGlobalBatch(() => scraperAPI.clearFinished(), '确定清空所有已完成、失败及取消的历史记录？')}
                    className="inline-flex items-center gap-1 rounded-xl border border-rose-300 bg-white px-3 py-1.5 text-xs font-semibold text-rose-600 hover:bg-rose-50 disabled:opacity-50"
                  >
                    <Trash2 size={12} />
                    全部删除 ({finishedCount})
                  </button>
                )}
              </>
            )}

            {(filter === 'pending' || filter === 'running') && (
              <button
                type="button"
                disabled={batchBusy || activeTaskCount === 0}
                onClick={() => runGlobalBatch(() => scraperAPI.cancelPending(), '确定取消所有排队及进行中的刮削任务？')}
                className="inline-flex items-center gap-1 rounded-xl border border-amber-300 bg-white px-3 py-1.5 text-xs font-semibold text-amber-600 hover:bg-amber-50 disabled:opacity-50"
              >
                <Ban size={12} />
                全部取消{activeTaskCount > 0 ? ` (${activeTaskCount})` : ''}
              </button>
            )}

            {filter === 'done' && (
              <button
                type="button"
                disabled={batchBusy || doneCount === 0}
                onClick={() => runGlobalBatch(() => scraperAPI.clearDone(), '确定清空所有已匹配完成的记录？')}
                className="inline-flex items-center gap-1 rounded-xl border border-rose-300 bg-white px-3 py-1.5 text-xs font-semibold text-rose-600 hover:bg-rose-50 disabled:opacity-50"
              >
                <Trash2 size={12} />
                全部删除{doneCount > 0 ? ` (${doneCount})` : ''}
              </button>
            )}

            {filter === 'failed' && (
              <>
                <button
                  type="button"
                  disabled={batchBusy || failedCount === 0}
                  onClick={() => runGlobalBatch(() => scraperAPI.retryFailed(), '确定重新入队所有失败任务？')}
                  className="inline-flex items-center gap-1 rounded-xl border border-brand-500/40 bg-white px-3 py-1.5 text-xs font-semibold text-brand-500 hover:bg-brand-50 disabled:opacity-50"
                >
                  <RefreshCw size={12} />
                  全部重试{failedCount > 0 ? ` (${failedCount})` : ''}
                </button>
                <button
                  type="button"
                  disabled={batchBusy || failedCount === 0}
                  onClick={() => runGlobalBatch(() => scraperAPI.clearFailed(), '确定清空所有失败记录？')}
                  className="inline-flex items-center gap-1 rounded-xl border border-rose-300 bg-white px-3 py-1.5 text-xs font-semibold text-rose-600 hover:bg-rose-50 disabled:opacity-50"
                >
                  <Trash2 size={12} />
                  全部删除{failedCount > 0 ? ` (${failedCount})` : ''}
                </button>
              </>
            )}

            {filter === 'canceled' && (
              <button
                type="button"
                disabled={batchBusy || canceledCount === 0}
                onClick={() => runGlobalBatch(() => scraperAPI.clearCanceled(), '确定清空所有已取消的任务记录？')}
                className="inline-flex items-center gap-1 rounded-xl border border-rose-300 bg-white px-3 py-1.5 text-xs font-semibold text-rose-600 hover:bg-rose-50 disabled:opacity-50"
              >
                <Trash2 size={12} />
                全部删除{canceledCount > 0 ? ` (${canceledCount})` : ''}
              </button>
            )}

            {/* 下拉批量操作菜单：随时可做任意全局操作 */}
            <details className="relative inline-block">
              <summary className="inline-flex cursor-pointer list-none items-center gap-1.5 rounded-xl border border-gray-200 bg-white px-3 py-1.5 text-xs font-semibold text-ink-100 shadow-sm transition hover:border-gray-300 hover:bg-gray-50 [&::-webkit-details-marker]:hidden">
                <Trash2 size={12} className="text-sand-500" />
                <span>批量清理</span>
              </summary>
              <div className="absolute right-0 top-9 z-30 min-w-44 rounded-xl border border-gray-200 bg-white p-1.5 shadow-xl backdrop-blur">
                {failedCount > 0 && (
                  <button
                    type="button"
                    disabled={batchBusy}
                    onClick={(e) => {
                      e.currentTarget.closest('details')?.removeAttribute('open')
                      runGlobalBatch(() => scraperAPI.retryFailed(), '确定重新入队所有失败任务？')
                    }}
                    className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-brand-500 hover:bg-brand-50"
                  >
                    <RefreshCw size={13} />
                    <span>重试所有失败 ({failedCount})</span>
                  </button>
                )}
                {activeTaskCount > 0 && (
                  <button
                    type="button"
                    disabled={batchBusy}
                    onClick={(e) => {
                      e.currentTarget.closest('details')?.removeAttribute('open')
                      runGlobalBatch(() => scraperAPI.cancelPending(), '确定取消所有排队及进行中的刮削任务？')
                    }}
                    className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-amber-600 hover:bg-amber-50"
                  >
                    <Ban size={13} />
                    <span>取消所有进行中 ({activeTaskCount})</span>
                  </button>
                )}
                <div className="my-1 border-t border-gray-100" />
                <button
                  type="button"
                  disabled={batchBusy}
                  onClick={(e) => {
                    e.currentTarget.closest('details')?.removeAttribute('open')
                    runGlobalBatch(() => scraperAPI.clearDone(), '确定清空所有已匹配完成的记录？')
                  }}
                  className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-ink-100 hover:bg-gray-50"
                >
                  <CheckCircle2 size={13} className="text-emerald-500" />
                  <span>清空已完成记录 ({doneCount})</span>
                </button>
                <button
                  type="button"
                  disabled={batchBusy}
                  onClick={(e) => {
                    e.currentTarget.closest('details')?.removeAttribute('open')
                    runGlobalBatch(() => scraperAPI.clearFailed(), '确定清空所有失败的记录？')
                  }}
                  className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-rose-500 hover:bg-rose-50"
                >
                  <AlertCircle size={13} />
                  <span>清空失败记录 ({failedCount})</span>
                </button>
                <button
                  type="button"
                  disabled={batchBusy}
                  onClick={(e) => {
                    e.currentTarget.closest('details')?.removeAttribute('open')
                    runGlobalBatch(() => scraperAPI.clearCanceled(), '确定清空所有已取消的任务记录？')
                  }}
                  className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-ink-100 hover:bg-gray-50"
                >
                  <Ban size={13} className="text-amber-500" />
                  <span>清空已取消记录 ({canceledCount})</span>
                </button>
                <button
                  type="button"
                  disabled={batchBusy}
                  onClick={(e) => {
                    e.currentTarget.closest('details')?.removeAttribute('open')
                    runGlobalBatch(() => scraperAPI.clearFinished(), '确定清空所有已完成、失败及取消的历史记录？')
                  }}
                  className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium text-rose-500 hover:bg-rose-50"
                >
                  <Trash2 size={13} />
                  <span>清空全部历史记录 ({finishedCount})</span>
                </button>
              </div>
            </details>
          </div>
        )}
      </div>

      {/* 4. Table */}
      <div className="glass-panel overflow-hidden !p-0 shadow-sm">
        {loading ? (
          <div className="flex justify-center py-16 text-ink-50">
            <Loader2 className="animate-spin text-brand-500" size={28} />
          </div>
        ) : filteredTasks.length === 0 ? (
          <div className="py-16 text-center text-xs text-sand-500">
            {search
              ? '没有找到符合搜索条件的刮削任务'
              : filter === 'all'
                ? '刮削队列为空，暂无进行或排队中的任务'
                : `「${FILTERS.find((f) => f.key === filter)?.label}」状态下暂无任务`}
          </div>
        ) : (
          <div className="table-scroll">
            <table className="min-w-[960px] w-full text-left text-sm">
              <thead className="border-b border-gray-200/80 bg-gray-50/50 text-[11px] font-bold uppercase tracking-wider text-sand-500">
                <tr>
                  <th className="w-10 px-3 py-3 text-center">
                    <input
                      type="checkbox"
                      checked={allCurrentChecked}
                      onChange={toggleSelectAll}
                      className="h-3.5 w-3.5 rounded border-gray-300 text-brand-500 focus:ring-brand-400 cursor-pointer"
                      title="全选 / 反选本页"
                    />
                  </th>
                  <th className="px-3 py-3">媒体文件</th>
                  <th className="px-3 py-3">所属媒体库</th>
                  <th className="px-3 py-3">刮削匹配结果</th>
                  <th className="px-3 py-3">识别源</th>
                  <th className="px-3 py-3">状态</th>
                  <th className="px-3 py-3">时间</th>
                  <th className="px-3 py-3 text-right">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {filteredTasks.map((task) => {
                  const status = taskStatusMeta(task.status)
                  const isSelected = selectedIds.has(task.id)

                  return (
                    <tr
                      key={task.id}
                      className={`transition-colors hover:bg-primary-400/5 ${
                        isSelected ? 'bg-primary-400/10' : ''
                      }`}
                    >
                      <td className="px-3 py-2.5 text-center">
                        <input
                          type="checkbox"
                          checked={isSelected}
                          onChange={() => toggleSelectRow(task.id)}
                          className="h-3.5 w-3.5 rounded border-gray-300 text-brand-500 focus:ring-brand-400 cursor-pointer"
                        />
                      </td>

                      {/* Media title & path */}
                      <td className="max-w-[240px] px-3 py-2.5">
                        <div className="flex items-center gap-2">
                          {TYPE_ICONS[task.media_type] || <Film size={14} className="text-gray-400" />}
                          <div className="min-w-0">
                            <span
                              onClick={() => setDetailTask(task)}
                              className="cursor-pointer truncate font-medium text-ink-600 hover:text-brand-500 hover:underline block"
                              title={task.media_title}
                            >
                              {task.media_title}
                            </span>
                            <span className="truncate font-mono text-[10px] text-gray-400 block" title={task.media_path}>
                              {task.media_path}
                            </span>
                          </div>
                        </div>
                      </td>

                      {/* Library */}
                      <td className="px-3 py-2.5 text-xs text-ink-100 whitespace-nowrap">
                        <span className="rounded-lg border border-gray-200 bg-gray-50 px-2 py-1 text-[11px] font-semibold text-ink-100">
                          {task.library_name || '媒体库'}
                        </span>
                      </td>

                      {/* Scraped matched result */}
                      <td className="max-w-[240px] px-3 py-2.5">
                        {task.matched_title ? (
                          <div className="flex items-center gap-2">
                            {task.poster_url ? (
                              <img
                                src={imageURL(task.poster_url)}
                                alt=""
                                className="h-10 w-7 rounded object-cover border border-gray-200 shrink-0"
                                onError={(e) => {
                                  e.currentTarget.style.display = 'none'
                                }}
                              />
                            ) : (
                              <div className="h-10 w-7 rounded bg-gray-100 flex items-center justify-center text-gray-400 shrink-0">
                                <ImageIcon size={12} />
                              </div>
                            )}
                            <div className="min-w-0">
                              <span className="font-bold text-ink-600 truncate block text-xs">
                                {task.matched_title}
                              </span>
                              {task.matched_year > 0 && (
                                <span className="text-[10px] text-gray-400">
                                  {task.matched_year} 年
                                </span>
                              )}
                            </div>
                          </div>
                        ) : (
                          <span className="text-xs text-sand-400 font-mono">
                            {task.status === 'pending' || task.status === 'running'
                              ? '等待识别…'
                              : '未匹配到结果'}
                          </span>
                        )}
                      </td>

                      {/* Provider */}
                      <td className="px-3 py-2.5 text-xs text-ink-100 whitespace-nowrap">
                        {task.provider ? (
                          <span className="rounded bg-brand-500/10 border border-brand-500/20 px-1.5 py-0.5 text-[10px] font-bold text-brand-500">
                            {PROVIDER_LABELS[task.provider] ?? task.provider}
                          </span>
                        ) : (
                          <span className="text-gray-300 text-xs">—</span>
                        )}
                      </td>

                      {/* Status & Error */}
                      <td className="px-3 py-2.5">
                        <div className="flex flex-col gap-0.5">
                          <span
                            className={`inline-flex w-fit items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-semibold ${status.cls}`}
                          >
                            {task.status === 'running' && (
                              <Loader2 size={10} className="animate-spin" />
                            )}
                            {task.status === 'done'
                              ? '已匹配'
                              : task.status === 'failed'
                                ? '未匹配'
                                : status.label}
                          </span>
                          {task.error && (
                            <span
                              onClick={() => setDetailTask(task)}
                              className="cursor-pointer truncate max-w-[180px] text-[10px] text-rose-500 hover:underline"
                              title={task.error}
                            >
                              {task.error}
                            </span>
                          )}
                        </div>
                      </td>

                      {/* Time */}
                      <td className="px-3 py-2.5 text-xs text-ink-50 whitespace-nowrap">
                        {formatTime(task.created_at)}
                      </td>

                      {/* Actions */}
                      <td className="px-3 py-2.5 text-right whitespace-nowrap">
                        <div className="flex items-center justify-end gap-1">
                          <button
                            type="button"
                            onClick={() => setDetailTask(task)}
                            className="rounded-lg p-1.5 text-gray-400 transition hover:bg-gray-100 hover:text-ink-600"
                            title="查看详情"
                          >
                            <Eye size={13} />
                          </button>

                          {(task.status === 'pending' || task.status === 'running') && (
                            <button
                              type="button"
                              onClick={() => cancelTask(task)}
                              className="rounded-lg border border-amber-200 px-2 py-1 text-xs font-semibold text-amber-600 transition hover:bg-amber-50"
                              title="取消刮削"
                            >
                              取消
                            </button>
                          )}

                          {(task.status === 'failed' ||
                            task.status === 'canceled' ||
                            task.status === 'done') && (
                            <button
                              type="button"
                              onClick={() => retryTask(task)}
                              className="rounded-lg border border-primary-400/50 bg-primary-400/5 px-2 py-1 text-xs font-semibold text-brand-500 transition hover:bg-primary-400/15"
                              title="重新刮削"
                            >
                              重刮
                            </button>
                          )}

                          {(task.status === 'done' ||
                            task.status === 'failed' ||
                            task.status === 'canceled') && (
                            <button
                              type="button"
                              onClick={() => deleteTask(task)}
                              className="rounded-lg p-1.5 text-gray-400 transition hover:bg-rose-50 hover:text-rose-500"
                              title="删除记录"
                            >
                              <Trash2 size={13} />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}

        {/* 5. Pagination */}
        {(snapshot?.total ?? 0) > 0 && (
          <div className="flex items-center justify-between border-t border-gray-200/80 bg-gray-50/40 px-4 py-3">
            <span className="text-xs text-sand-500">
              共 {snapshot?.total ?? 0} 条 · 第 {page} / {totalPages} 页
            </span>
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                disabled={page <= 1 || loading}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                className="inline-flex items-center rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-xs font-semibold text-ink-100 transition hover:bg-gray-50 disabled:opacity-40"
              >
                上一页
              </button>
              <button
                type="button"
                disabled={page >= totalPages || loading}
                onClick={() => setPage((p) => p + 1)}
                className="inline-flex items-center rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-xs font-semibold text-ink-100 transition hover:bg-gray-50 disabled:opacity-40"
              >
                下一页
              </button>
            </div>
          </div>
        )}
      </div>

      {/* 6. Task Detail Modal */}
      {detailTask && (
        <ScrapeDetailModal
          task={detailTask}
          onClose={() => setDetailTask(null)}
          onRetry={retryTask}
          onCancel={cancelTask}
          onDelete={deleteTask}
          onCopy={copyText}
        />
      )}
    </div>
  )
}
