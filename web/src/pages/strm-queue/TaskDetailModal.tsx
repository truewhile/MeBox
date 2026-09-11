import { AlertCircle, Ban, Copy, RefreshCw, Trash2, X } from 'lucide-react'

import type { StrmTask } from '../../types/strm'
import { STRM_PROVIDER_LABELS } from '../../types/strm'
import { formatBytes, formatTime, taskStatusMeta } from '../StrmManagePage'
import { getFileIcon } from './taskFileIcon'

export function TaskDetailModal({
  task,
  isDownload,
  onClose,
  onRetry,
  onCancel,
  onDelete,
  onCopy,
}: {
  task: StrmTask
  isDownload: boolean
  onClose: () => void
  onRetry: (t: StrmTask) => void
  onCancel: (t: StrmTask) => void
  onDelete: (t: StrmTask) => void
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
            {getFileIcon(task.file_name)}
            <h3 className="font-display text-base font-bold text-ink-600">任务详情</h3>
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
          {/* Main Info Box */}
          <div className="rounded-2xl border border-gray-100 bg-gray-50/70 p-4 space-y-3">
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">任务 ID</span>
              <span className="font-mono text-ink-100 select-all">{task.id}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">文件名称</span>
              <span className="font-bold text-ink-600 select-all">{task.file_name}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">文件大小</span>
              <span className="font-mono text-ink-100">{formatBytes(task.size)}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">当前状态</span>
              <span
                className={`inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-semibold ${status.cls}`}
              >
                {status.label}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-sand-500 font-medium">云盘提供方</span>
              <span className="font-medium text-ink-100">
                {STRM_PROVIDER_LABELS[task.provider] ?? task.provider}
              </span>
            </div>
            {task.retry_count > 0 && (
              <div className="flex items-center justify-between">
                <span className="text-sand-500 font-medium">已重试次数</span>
                <span className="font-bold text-amber-600">{task.retry_count} 次</span>
              </div>
            )}
          </div>

          {/* Paths */}
          <div className="space-y-2">
            <div className="flex items-center justify-between text-sand-500 font-medium">
              <span>{isDownload ? '本地输出目标路径' : '本地来源路径'}</span>
              <button
                type="button"
                onClick={() => onCopy(task.local_path, '本地路径')}
                className="inline-flex items-center gap-1 text-brand-500 hover:underline"
              >
                <Copy size={11} /> 复制
              </button>
            </div>
            <div className="rounded-xl border border-gray-200 bg-gray-50/50 p-3 font-mono text-[11px] text-ink-600 break-all select-all">
              {task.local_path}
            </div>
          </div>

          <div className="space-y-2">
            <div className="flex items-center justify-between text-sand-500 font-medium">
              <span>远端网盘路径</span>
              <button
                type="button"
                onClick={() => onCopy(task.remote_path, '远端路径')}
                className="inline-flex items-center gap-1 text-brand-500 hover:underline"
              >
                <Copy size={11} /> 复制
              </button>
            </div>
            <div className="rounded-xl border border-gray-200 bg-gray-50/50 p-3 font-mono text-[11px] text-ink-600 break-all select-all">
              {task.remote_path}
            </div>
          </div>

          {/* Error Message Box */}
          {task.error && (
            <div className="space-y-2">
              <div className="flex items-center justify-between text-rose-500 font-medium">
                <span className="flex items-center gap-1">
                  <AlertCircle size={13} /> 错误详情
                </span>
                <button
                  type="button"
                  onClick={() => onCopy(task.error!, '错误信息')}
                  className="inline-flex items-center gap-1 text-rose-500 hover:underline"
                >
                  <Copy size={11} /> 复制错误
                </button>
              </div>
              <div className="rounded-xl border border-rose-200 bg-rose-50/60 p-3 font-mono text-[11px] text-rose-700 break-all select-all whitespace-pre-wrap">
                {task.error}
              </div>
            </div>
          )}

          {/* Timeline */}
          <div className="grid grid-cols-2 gap-3 pt-2 text-[11px] text-sand-500 border-t border-gray-100">
            <div>创建时间：{formatTime(task.created_at)}</div>
            {task.started_at && <div>开始时间：{formatTime(task.started_at)}</div>}
            {task.finished_at && <div>结束时间：{formatTime(task.finished_at)}</div>}
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

            {(task.status === 'failed' || task.status === 'canceled') && (
              <button
                type="button"
                onClick={() => onRetry(task)}
                className="neon-button !py-2 !px-4 text-xs font-semibold"
              >
                <RefreshCw size={13} />
                重新入队
              </button>
            )}

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
