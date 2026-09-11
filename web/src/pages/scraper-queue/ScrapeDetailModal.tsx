import { Link } from 'react-router-dom'
import { AlertCircle, Ban, Copy, ExternalLink, RefreshCw, Sparkles, Trash2, X } from 'lucide-react'

import { imageURL } from '../../api/client'
import type { ScrapeTask } from '../../types/scraper'
import { formatTime, taskStatusMeta } from '../StrmManagePage'
import { PROVIDER_LABELS, TYPE_LABELS } from './scrapeLabels'

export function ScrapeDetailModal({
  task,
  onClose,
  onRetry,
  onCancel,
  onDelete,
  onCopy,
}: {
  task: ScrapeTask
  onClose: () => void
  onRetry: (t: ScrapeTask) => void
  onCancel: (t: ScrapeTask) => void
  onDelete: (t: ScrapeTask) => void
  onCopy: (text: string, label: string) => void
}) {
  const status = taskStatusMeta(task.status)

  return (
    <div
      className="fixed inset-0 z-[110] flex items-center justify-center bg-black/40 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="w-full max-w-xl rounded-3xl border border-gray-200 bg-white shadow-2xl overflow-hidden animate-in fade-in zoom-in-95 duration-150"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
          <div className="flex items-center gap-2">
            <Sparkles size={16} className="text-brand-500" />
            <h3 className="font-display text-base font-bold text-ink-600">刮削任务详情</h3>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl p-1 text-gray-400 hover:bg-gray-100 hover:text-ink-600 transition"
          >
            <X size={18} />
          </button>
        </div>

        <div className="space-y-4 p-6 max-h-[70vh] overflow-y-auto text-xs">
          {/* Matched Poster / Info Banner */}
          {task.matched_title ? (
            <div className="flex gap-4 rounded-2xl border border-brand-500/20 bg-primary-400/5 p-4">
              {task.poster_url && (
                <img
                  src={imageURL(task.poster_url)}
                  alt=""
                  className="h-28 w-20 rounded-xl object-cover border border-brand-500/30 shadow-md shrink-0"
                />
              )}
              <div className="space-y-1.5 min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="rounded bg-brand-500 px-2 py-0.5 text-[10px] font-bold text-white uppercase">
                    已匹配
                  </span>
                  {task.provider && (
                    <span className="rounded border border-gray-200 bg-white px-2 py-0.5 text-[10px] font-semibold text-ink-600">
                      {PROVIDER_LABELS[task.provider] ?? task.provider}
                    </span>
                  )}
                </div>
                <h4 className="font-display text-base font-extrabold text-ink-600 truncate">
                  {task.matched_title}
                </h4>
                <div className="flex items-center gap-3 text-sand-500 text-[11px]">
                  {task.matched_year > 0 && <span>年份：{task.matched_year}</span>}
                  <span>类型：{TYPE_LABELS[task.media_type] ?? task.media_type}</span>
                </div>
                {task.media_id && (
                  <Link
                    to={`/media/${task.media_id}`}
                    target="_blank"
                    className="inline-flex items-center gap-1 text-brand-500 font-semibold hover:underline pt-1"
                  >
                    <span>在媒体详情中查看</span>
                    <ExternalLink size={11} />
                  </Link>
                )}
              </div>
            </div>
          ) : null}

          {/* Media Info Box */}
          <div className="rounded-2xl border border-gray-100 bg-gray-50/70 p-4 space-y-3">
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">原始媒体标题</span>
              <span className="font-bold text-ink-600 select-all">{task.media_title}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">所属媒体库</span>
              <span className="font-medium text-ink-100">{task.library_name}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">媒体库类型</span>
              <span className="font-medium text-ink-100">
                {TYPE_LABELS[task.media_type] ?? task.media_type}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">当前状态</span>
              <span
                className={`inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-semibold ${status.cls}`}
              >
                {task.status === 'done' ? '已匹配' : task.status === 'failed' ? '未匹配' : status.label}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">剧照/海报刮削</span>
              <span className="font-medium text-ink-100">
                {task.episode_images ? '开启' : '关闭'}
              </span>
            </div>
          </div>

          {/* File path */}
          <div className="space-y-2">
            <div className="flex items-center justify-between text-sand-500 font-medium">
              <span>磁盘文件路径</span>
              <button
                type="button"
                onClick={() => onCopy(task.media_path, '文件路径')}
                className="inline-flex items-center gap-1 text-brand-500 hover:underline"
              >
                <Copy size={11} /> 复制
              </button>
            </div>
            <div className="rounded-xl border border-gray-200 bg-gray-50/50 p-3 font-mono text-[11px] text-ink-600 break-all select-all">
              {task.media_path}
            </div>
          </div>

          {/* Error Message Box */}
          {task.error && (
            <div className="space-y-2">
              <div className="flex items-center justify-between text-rose-500 font-medium">
                <span className="flex items-center gap-1">
                  <AlertCircle size={13} /> 刮削未匹配 / 异常详情
                </span>
                <button
                  type="button"
                  onClick={() => onCopy(task.error, '错误日志')}
                  className="inline-flex items-center gap-1 text-rose-500 hover:underline"
                >
                  <Copy size={11} /> 复制日志
                </button>
              </div>
              <div className="rounded-xl border border-rose-200 bg-rose-50/60 p-3 font-mono text-[11px] text-rose-700 break-all select-all whitespace-pre-wrap">
                {task.error}
              </div>
            </div>
          )}

          {/* Timeline */}
          <div className="grid grid-cols-2 gap-3 pt-2 text-[11px] text-sand-500 border-t border-gray-100">
            <div>入队时间：{formatTime(task.created_at)}</div>
            {task.started_at && <div>开始刮削：{formatTime(task.started_at)}</div>}
            {task.finished_at && <div>完成时间：{formatTime(task.finished_at)}</div>}
          </div>
        </div>

        {/* Footer Actions */}
        <div className="flex items-center justify-between border-t border-gray-100 px-6 py-4 bg-gray-50/50">
          <div>
            {(task.status === 'done' ||
              task.status === 'failed' ||
              task.status === 'canceled') && (
              <button
                type="button"
                onClick={() => onDelete(task)}
                className="inline-flex items-center gap-1 rounded-xl border border-rose-200 bg-white px-3 py-2 text-xs font-semibold text-rose-500 hover:bg-rose-50 transition"
              >
                <Trash2 size={13} />
                删除记录
              </button>
            )}
          </div>

          <div className="flex items-center gap-2">
            {(task.status === 'pending' || task.status === 'running') && (
              <button
                type="button"
                onClick={() => onCancel(task)}
                className="inline-flex items-center gap-1 rounded-xl border border-amber-200 bg-white px-4 py-2 text-xs font-semibold text-amber-600 hover:bg-amber-50 transition"
              >
                <Ban size={13} />
                取消任务
              </button>
            )}

            <button
              type="button"
              onClick={() => onRetry(task)}
              className="neon-button !py-2 !px-4 text-xs font-semibold"
            >
              <RefreshCw size={13} />
              重新刮削
            </button>

            <button
              type="button"
              onClick={onClose}
              className="rounded-xl border border-gray-200 bg-white px-4 py-2 text-xs font-semibold text-ink-100 hover:bg-gray-50 transition"
            >
              关闭
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
