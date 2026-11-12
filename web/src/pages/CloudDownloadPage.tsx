// 115 云下载页面：提交磁力/ed2k/HTTP/.torrent → 115 离线下载 → 自动 STRM 同步 →
// 自动扫描 → 自动刮削。链路各阶段在任务列表里可见。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import toast from 'react-hot-toast'
import {
  AlertCircle,
  CheckCircle2,
  CloudDownload,
  FileUp,
  Loader2,
  RefreshCw,
  RotateCcw,
  Trash2,
  XCircle,
} from 'lucide-react'

import { cloudDownloadAPI } from '../api/cloudDownload'
import { strmAPI } from '../api/strm'
import { PageHeader } from '../components/PageHeader'
import type { StrmSyncPath } from '../types/strm'
import type {
  CloudDownloadQuota,
  CloudDownloadSubmitResult,
  CloudDownloadTask,
} from '../types/cloudDownload'
import { apiErrorMessage, formatBytes, formatTime } from './StrmManagePage'

const PAGE_SIZE = 20

// 阶段与状态合并成一句人话，避免用户对着 status/stage 两个字段猜。
function taskPhase(task: CloudDownloadTask): { label: string; cls: string; spinner?: boolean } {
  if (task.status === 'failed') return { label: '下载失败', cls: 'bg-rose-100 text-rose-600' }
  if (task.status === 'canceled') return { label: '已取消', cls: 'bg-gray-100 text-gray-500' }
  if (task.status === 'queued' || task.status === 'submitted') {
    return { label: '等待 115 受理', cls: 'bg-amber-100 text-amber-600', spinner: true }
  }
  if (task.status === 'downloading') {
    return { label: `下载中 ${task.percent ?? 0}%`, cls: 'bg-brand-100 text-brand-600', spinner: true }
  }
  // status = succeeded：看后处理阶段
  switch (task.stage) {
    case 'waiting_sync':
      return { label: '等待同步', cls: 'bg-sky-100 text-sky-600', spinner: true }
    case 'syncing':
      return { label: 'STRM 同步中', cls: 'bg-sky-100 text-sky-600', spinner: true }
    case 'scanning':
      return { label: '扫描媒体库', cls: 'bg-indigo-100 text-indigo-600', spinner: true }
    case 'scraping':
      return { label: '刮削中', cls: 'bg-violet-100 text-violet-600', spinner: true }
    case 'done':
      return { label: '完成', cls: 'bg-emerald-100 text-emerald-600' }
    case 'failed':
      return { label: '后处理失败', cls: 'bg-rose-100 text-rose-600' }
    case 'na':
      return { label: '未接入媒体库', cls: 'bg-amber-100 text-amber-600' }
    default:
      return { label: '待后处理', cls: 'bg-sky-100 text-sky-600', spinner: true }
  }
}

function sourceLabel(task: CloudDownloadTask): string {
  if (task.source_type === 'torrent') return task.torrent_name || task.source_url || '.torrent'
  return task.source_url
}

export function CloudDownloadPage() {
  const [paths, setPaths] = useState<StrmSyncPath[]>([])
  const [syncPathID, setSyncPathID] = useState('')
  const [urls, setUrls] = useState('')
  const [torrentFile, setTorrentFile] = useState<File | null>(null)
  const [quota, setQuota] = useState<CloudDownloadQuota | null>(null)
  const [quotaError, setQuotaError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [lastResult, setLastResult] = useState<CloudDownloadSubmitResult | null>(null)

  const [tasks, setTasks] = useState<CloudDownloadTask[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState('')
  const [loading, setLoading] = useState(true)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [busyID, setBusyID] = useState('')
  const refreshSeq = useRef(0)

  const targets = useMemo(
    () => paths.filter((p) => p.provider === 'cloud115' && p.enabled),
    [paths],
  )
  const selectedPath = useMemo(
    () => targets.find((p) => p.id === syncPathID) ?? null,
    [targets, syncPathID],
  )

  const loadPaths = useCallback(async () => {
    try {
      const list = await strmAPI.listPaths()
      setPaths(list)
      setSyncPathID((prev) => {
        if (prev && list.some((p) => p.id === prev)) return prev
        const first = list.find((p) => p.provider === 'cloud115' && p.enabled)
        return first?.id ?? ''
      })
    } catch (err) {
      toast.error(apiErrorMessage(err))
    }
  }, [])

  const loadTasks = useCallback(
    async (quiet = false) => {
      const seq = ++refreshSeq.current
      if (!quiet) setLoading(true)
      try {
        const data = await cloudDownloadAPI.list({
          status: statusFilter || undefined,
          page,
          page_size: PAGE_SIZE,
        })
        if (seq !== refreshSeq.current) return
        setTasks(data.items ?? [])
        setTotal(data.total ?? 0)
      } catch (err) {
        if (!quiet) toast.error(apiErrorMessage(err))
      } finally {
        if (seq === refreshSeq.current) setLoading(false)
      }
    },
    [page, statusFilter],
  )

  useEffect(() => {
    void loadPaths()
  }, [loadPaths])

  useEffect(() => {
    void loadTasks()
  }, [loadTasks])

  useEffect(() => {
    if (!autoRefresh) return
    const timer = window.setInterval(() => {
      void loadTasks(true)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [autoRefresh, loadTasks])

  // 切换目标目录时刷新配额（配额按账号统计）。
  useEffect(() => {
    setQuota(null)
    setQuotaError('')
    if (!selectedPath?.account_id) return
    let cancelled = false
    cloudDownloadAPI
      .quota(selectedPath.account_id)
      .then((q) => {
        if (!cancelled) setQuota(q)
      })
      .catch((err) => {
        if (!cancelled) setQuotaError(apiErrorMessage(err))
      })
    return () => {
      cancelled = true
    }
  }, [selectedPath?.account_id])

  const submitLinks = async () => {
    if (!syncPathID) {
      toast.error('请选择目标同步目录')
      return
    }
    const lines = urls
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
    if (lines.length === 0) {
      toast.error('请粘贴至少一条链接')
      return
    }
    setSubmitting(true)
    try {
      const res = await cloudDownloadAPI.submit({ sync_path_id: syncPathID, urls: lines })
      setLastResult(res)
      if (res.accepted > 0) {
        toast.success(`已提交 ${res.accepted} 条任务`)
        setUrls('')
      } else {
        toast.error('没有任务被 115 受理，请查看下方明细')
      }
      await loadTasks(true)
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  const submitTorrent = async () => {
    if (!syncPathID) {
      toast.error('请选择目标同步目录')
      return
    }
    if (!torrentFile) {
      toast.error('请选择 .torrent 文件')
      return
    }
    setSubmitting(true)
    try {
      const res = await cloudDownloadAPI.submitTorrent(torrentFile, syncPathID)
      setLastResult(res)
      toast.success('种子已提交')
      setTorrentFile(null)
      await loadTasks(true)
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  const act = async (id: string, fn: () => Promise<unknown>, okText: string) => {
    setBusyID(id)
    try {
      await fn()
      toast.success(okText)
      await loadTasks(true)
    } catch (err) {
      toast.error(apiErrorMessage(err))
    } finally {
      setBusyID('')
    }
  }

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <div className="mx-auto w-full max-w-6xl space-y-6 px-4 py-6">
      <PageHeader
        title="云下载"
        description="提交磁力 / ed2k / 直链或 .torrent，115 离线完成后自动同步 STRM、扫描媒体库并刮削"
        backTo="/strm"
        backLabel="STRM 管理"
        actions={
          <button
            type="button"
            onClick={() => void loadTasks()}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 px-3 py-1.5 text-sm text-ink-600 hover:bg-gray-50"
          >
            <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
            刷新
          </button>
        }
      />

      {/* 提交面板 */}
      <section className="space-y-4 rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="space-y-1">
            <span className="text-xs font-medium text-ink-50">目标同步目录（115）</span>
            <select
              value={syncPathID}
              onChange={(e) => setSyncPathID(e.target.value)}
              className="w-full rounded-lg border border-gray-200 px-3 py-2 text-sm"
            >
              <option value="">请选择…</option>
              {targets.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                  {p.remote_display_path ? ` · ${p.remote_display_path}` : ''}
                </option>
              ))}
            </select>
          </label>
          <div className="space-y-1">
            <span className="text-xs font-medium text-ink-50">云下载配额</span>
            <div className="flex h-[38px] items-center gap-2 rounded-lg border border-gray-100 bg-gray-50 px-3 text-sm text-ink-600">
              {quota ? (
                <>
                  <span className="font-semibold text-ink-600">{quota.surplus}</span>
                  <span className="text-ink-50">/ {quota.count} 剩余</span>
                  {quota.surplus <= 0 ? (
                    <span className="ml-auto text-xs text-rose-500">配额已用尽</span>
                  ) : null}
                </>
              ) : quotaError ? (
                <span className="truncate text-xs text-amber-600">{quotaError}</span>
              ) : (
                <span className="text-xs text-ink-50">选择目录后加载</span>
              )}
            </div>
          </div>
        </div>

        <label className="block space-y-1">
          <span className="text-xs font-medium text-ink-50">
            链接（每行一条，支持磁力 / ed2k / http(s) / ftp，单批最多 115 条）
          </span>
          <textarea
            value={urls}
            onChange={(e) => setUrls(e.target.value)}
            rows={4}
            placeholder={'magnet:?xt=urn:btih:…\ned2k://|file|…\nhttps://example.com/file.mkv'}
            className="w-full resize-y rounded-lg border border-gray-200 px-3 py-2 font-mono text-xs"
          />
        </label>

        <div className="flex flex-wrap items-center gap-3">
          <button
            type="button"
            disabled={submitting || !syncPathID}
            onClick={() => void submitLinks()}
            className="inline-flex items-center gap-1.5 rounded-lg bg-brand-500 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
          >
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <CloudDownload className="h-4 w-4" />}
            提交链接
          </button>

          <span className="text-xs text-ink-50">或</span>

          <label className="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-gray-200 px-3 py-2 text-sm text-ink-600 hover:bg-gray-50">
            <FileUp className="h-4 w-4" />
            {torrentFile ? torrentFile.name : '选择 .torrent'}
            <input
              type="file"
              accept=".torrent"
              className="hidden"
              onChange={(e) => setTorrentFile(e.target.files?.[0] ?? null)}
            />
          </label>
          <button
            type="button"
            disabled={submitting || !syncPathID || !torrentFile}
            onClick={() => void submitTorrent()}
            className="rounded-lg border border-brand-200 px-3 py-2 text-sm text-brand-600 disabled:opacity-40"
          >
            上传种子
          </button>
        </div>

        <p className="text-xs text-ink-50">
          说明：115 开放平台的磁力链路不支持选择部分文件，磁力任务会整包下载；.torrent 目前也按全选提交。
        </p>

        {lastResult ? (
          <div className="space-y-1 rounded-lg bg-gray-50 p-3 text-xs">
            <div className="font-medium text-ink-600">
              成功 {lastResult.accepted} 条，失败 {lastResult.rejected} 条
            </div>
            {lastResult.items?.map((item, idx) => (
              <div key={`${item.source}-${idx}`} className="flex gap-2">
                {item.ok ? (
                  <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0 text-emerald-500" />
                ) : (
                  <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-rose-500" />
                )}
                <span className="break-all text-ink-600">{item.source}</span>
                {item.error ? <span className="text-rose-500">{item.error}</span> : null}
              </div>
            ))}
            {lastResult.skipped?.map((item, idx) => (
              <div key={`skip-${idx}`} className="flex gap-2 text-ink-50">
                <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-500" />
                <span className="break-all">{item.raw}</span>
                <span>{item.reason}</span>
              </div>
            ))}
          </div>
        ) : null}
      </section>

      {/* 任务列表 */}
      <section className="space-y-3 rounded-xl border border-gray-200 bg-white p-4 shadow-sm">
        <div className="flex flex-wrap items-center gap-2">
          <select
            value={statusFilter}
            onChange={(e) => {
              setStatusFilter(e.target.value)
              setPage(1)
            }}
            className="rounded-lg border border-gray-200 px-2 py-1.5 text-sm"
          >
            <option value="">全部状态</option>
            <option value="submitted">已提交</option>
            <option value="downloading">下载中</option>
            <option value="succeeded">已完成</option>
            <option value="failed">失败</option>
            <option value="canceled">已取消</option>
          </select>
          <label className="inline-flex items-center gap-1.5 text-xs text-ink-50">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            自动刷新
          </label>
          <span className="ml-auto text-xs text-ink-50">共 {total} 条</span>
          <button
            type="button"
            onClick={() =>
              void act('__clear__', () => cloudDownloadAPI.clearFinished(), '已清空失败/取消任务')
            }
            className="rounded-lg border border-gray-200 px-2.5 py-1.5 text-xs text-ink-600 hover:bg-gray-50"
          >
            清空失败/已取消
          </button>
        </div>

        <div className="overflow-x-auto">
          <table className="w-full min-w-[860px] text-left text-sm">
            <thead className="text-xs text-ink-50">
              <tr className="border-b border-gray-100">
                <th className="py-2 pr-3 font-medium">来源</th>
                <th className="py-2 pr-3 font-medium">目标目录</th>
                <th className="py-2 pr-3 font-medium">大小</th>
                <th className="py-2 pr-3 font-medium">状态</th>
                <th className="py-2 pr-3 font-medium">提交时间</th>
                <th className="py-2 pr-3 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {tasks.length === 0 ? (
                <tr>
                  <td colSpan={6} className="py-8 text-center text-sm text-ink-50">
                    {loading ? '加载中…' : '暂无任务'}
                  </td>
                </tr>
              ) : (
                tasks.map((task) => {
                  const phase = taskPhase(task)
                  const busy = busyID === task.id
                  const terminal =
                    task.status === 'succeeded' ||
                    task.status === 'failed' ||
                    task.status === 'canceled'
                  return (
                    <tr key={task.id} className="border-b border-gray-50 align-top">
                      <td className="max-w-[320px] py-2.5 pr-3">
                        <div className="truncate text-ink-600" title={sourceLabel(task)}>
                          {sourceLabel(task)}
                        </div>
                        <div className="text-xs text-ink-50">
                          {task.source_type.toUpperCase()}
                          {task.info_hash ? ` · ${task.info_hash.slice(0, 12)}…` : ''}
                        </div>
                        {task.error ? (
                          <div className="mt-0.5 text-xs text-rose-500">{task.error}</div>
                        ) : null}
                        {task.stage_error ? (
                          <div className="mt-0.5 text-xs text-amber-600">{task.stage_error}</div>
                        ) : null}
                      </td>
                      <td className="py-2.5 pr-3 text-xs text-ink-600">
                        <div>{task.sync_path_name || task.sync_path_id.slice(0, 8)}</div>
                        {task.remote_display_path ? (
                          <div className="text-ink-50">{task.remote_display_path}</div>
                        ) : null}
                      </td>
                      <td className="py-2.5 pr-3 text-xs text-ink-600">
                        {task.size_bytes ? formatBytes(task.size_bytes) : '-'}
                      </td>
                      <td className="py-2.5 pr-3">
                        <span
                          className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs ${phase.cls}`}
                        >
                          {phase.spinner ? <Loader2 className="h-3 w-3 animate-spin" /> : null}
                          {phase.label}
                        </span>
                        {task.stage === 'done' ? (
                          <div className="mt-0.5 text-[11px] text-ink-50">
                            新增 {task.scan_added} · 刮削 {task.scrape_queued}
                          </div>
                        ) : null}
                      </td>
                      <td className="py-2.5 pr-3 text-xs text-ink-50">{formatTime(task.created_at)}</td>
                      <td className="py-2.5 pr-3">
                        <div className="flex flex-wrap items-center gap-1.5">
                          {task.status === 'failed' ? (
                            <button
                              type="button"
                              disabled={busy}
                              onClick={() =>
                                void act(task.id, () => cloudDownloadAPI.retry(task.id), '已重新提交')
                              }
                              className="inline-flex items-center gap-1 rounded-md border border-gray-200 px-2 py-1 text-xs text-ink-600 hover:bg-gray-50 disabled:opacity-50"
                            >
                              <RotateCcw className="h-3 w-3" />
                              重试
                            </button>
                          ) : null}
                          {(task.stage === 'failed' || task.stage === 'na') ? (
                            <button
                              type="button"
                              disabled={busy}
                              onClick={() =>
                                void act(
                                  task.id,
                                  () => cloudDownloadAPI.retryPipeline(task.id),
                                  '已重新推进后处理',
                                )
                              }
                              className="inline-flex items-center gap-1 rounded-md border border-gray-200 px-2 py-1 text-xs text-ink-600 hover:bg-gray-50 disabled:opacity-50"
                            >
                              <RefreshCw className="h-3 w-3" />
                              重做后处理
                            </button>
                          ) : null}
                          {!terminal ? (
                            <button
                              type="button"
                              disabled={busy}
                              onClick={() =>
                                void act(task.id, () => cloudDownloadAPI.cancel(task.id), '已取消')
                              }
                              className="inline-flex items-center gap-1 rounded-md border border-gray-200 px-2 py-1 text-xs text-amber-600 hover:bg-gray-50 disabled:opacity-50"
                            >
                              <XCircle className="h-3 w-3" />
                              取消
                            </button>
                          ) : (
                            <button
                              type="button"
                              disabled={busy}
                              onClick={() =>
                                void act(task.id, () => cloudDownloadAPI.remove(task.id), '已删除')
                              }
                              className="inline-flex items-center gap-1 rounded-md border border-gray-200 px-2 py-1 text-xs text-rose-500 hover:bg-gray-50 disabled:opacity-50"
                            >
                              <Trash2 className="h-3 w-3" />
                              删除
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </table>
        </div>

        {totalPages > 1 ? (
          <div className="flex items-center justify-end gap-2 text-xs text-ink-50">
            <button
              type="button"
              disabled={page <= 1}
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              className="rounded-md border border-gray-200 px-2 py-1 disabled:opacity-40"
            >
              上一页
            </button>
            <span>
              {page} / {totalPages}
            </span>
            <button
              type="button"
              disabled={page >= totalPages}
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              className="rounded-md border border-gray-200 px-2 py-1 disabled:opacity-40"
            >
              下一页
            </button>
          </div>
        ) : null}
      </section>
    </div>
  )
}
