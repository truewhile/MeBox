import { useCallback, useEffect, useState } from 'react'
import { Activity, AlertTriangle, CheckCircle2, Clock3, RefreshCw } from 'lucide-react'

import { tasksAPI, type BackgroundTask, type TaskSnapshot } from '../api/tasks'
import { useWebSocket } from '../hooks/useWebSocket'

function statusLabel(status: string) {
  switch (status) {
    case 'running': return '运行中'
    case 'completed': return '已完成'
    case 'failed': return '失败'
    case 'interrupted': return '进程中断'
    default: return status
  }
}

function TaskRow({ task }: { task: BackgroundTask }) {
  const Icon = task.status === 'running' ? Activity : task.status === 'completed' ? CheckCircle2 : AlertTriangle
  const tone = task.status === 'running' ? 'text-blue-500' : task.status === 'completed' ? 'text-emerald-500' : 'text-amber-600'
  return (
    <article className="rounded-xl border border-sand-200 bg-white/60 p-3">
      <div className="flex items-start gap-3">
        <Icon size={17} className={`mt-0.5 shrink-0 ${tone}`} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="break-words text-sm font-semibold text-ink-600">{task.name}</h3>
            <span className={`text-xs font-semibold ${tone}`}>{statusLabel(task.status)}</span>
          </div>
          <p className="mt-1 text-xs text-ink-50">{task.message || task.stage || '—'} · {new Date(task.started_at).toLocaleString()}</p>
          {task.error && <p className="mt-2 break-words text-xs text-red-500">{task.error}</p>}
          {task.details && task.details.length > 0 && (
            <details className="mt-2 text-xs text-ink-50"><summary className="cursor-pointer">查看执行详情（{task.details.length}）</summary>
              <ul className="mt-2 space-y-1">{task.details.map((line, index) => <li key={`${index}-${line}`} className="break-words">{line}</li>)}</ul>
            </details>
          )}
        </div>
      </div>
    </article>
  )
}

export function BackgroundTasksPanel() {
  const [snapshot, setSnapshot] = useState<TaskSnapshot>({ active: [], recent: [] })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      const result = await tasksAPI.snapshot()
      setSnapshot({ active: result.active ?? [], recent: result.recent ?? [] })
      setError('')
    } catch {
      setError('读取后台任务失败，请刷新重试')
    } finally { setLoading(false) }
  }, [])
  useEffect(() => { void refresh() }, [refresh])
  useWebSocket((topic, payload) => {
    if (topic !== 'task' || !payload || typeof payload !== 'object') return
    const task = payload as BackgroundTask
    if (!task.id || !task.status) return
    setSnapshot((current) => {
      const active = current.active.filter((item) => item.id !== task.id)
      const recent = current.recent.filter((item) => item.id !== task.id)
      if (task.status === 'running') active.unshift(task)
      else recent.unshift(task)
      recent.sort((left, right) => right.started_at.localeCompare(left.started_at))
      return { active, recent: recent.slice(0, 30) }
    })
  })

  return (
    <section className="glass-panel space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2"><Activity size={18} className="text-brand-500" /><h2 className="font-display text-xl font-bold text-ink-600">后台扫描与整理</h2></div>
        <button type="button" className="btn-outline" onClick={() => void refresh()} disabled={loading}><RefreshCw size={14} />刷新</button>
      </div>
      {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
      {snapshot.active.length > 0 && <div className="space-y-2"><h3 className="text-xs font-bold uppercase tracking-wide text-ink-50">进行中</h3>{snapshot.active.map((task) => <TaskRow key={task.id} task={task} />)}</div>}
      <div className="space-y-2">
        <h3 className="flex items-center gap-1 text-xs font-bold uppercase tracking-wide text-ink-50"><Clock3 size={13} />最近任务</h3>
        {snapshot.recent.length > 0 ? snapshot.recent.map((task) => <TaskRow key={task.id} task={task} />) : !error && <p className="rounded-xl border border-dashed border-sand-300 px-4 py-6 text-center text-sm text-ink-50">{loading ? '正在读取任务…' : '暂无历史任务'}</p>}
      </div>
    </section>
  )
}
