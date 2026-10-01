import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Clock, Film, Loader2, Star, Tv } from 'lucide-react'
import { ARTWORK, imageURL } from '../api/client'
import { historyAPI } from '../api/history'
import type { HistoryStats } from '../types/history'
import { PageHeader } from '../components/PageHeader'
import { usePermission } from '../hooks/usePermission'

// WatchStatsPage 展示个人观看统计。
//
// 只消费已有数据（PlaybackHistory 聚合），因此这里没有任何写操作；图表也用
// 纯 CSS 柱状呈现，不引入图表库 —— 30 根柱子的信息量不值得多一个依赖。
export function WatchStatsPage() {
  const navigate = useNavigate()
  const canViewHistory = usePermission('can_view_history')
  const [stats, setStats] = useState<HistoryStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)

  // Redirect denied users before issuing any API calls.
  useEffect(() => {
    if (!canViewHistory) {
      navigate('/', { replace: true })
    }
  }, [canViewHistory, navigate])

  const load = useCallback(async () => {
    try {
      const data = await historyAPI.stats()
      setStats(data)
      setFailed(false)
    } catch {
      setFailed(true)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) {
    return (
      <div className="flex items-center justify-center py-32 text-[var(--app-muted)]">
        <Loader2 className="animate-spin" />
      </div>
    )
  }

  if (failed || !stats) {
    return (
      <div className="space-y-6">
        <PageHeader title="观看统计" description="你的观看时长、类型分布与最近记录" />
        <p className="glass-panel p-6 text-center text-sm text-[var(--app-muted)]">
          统计加载失败，请稍后重试。
        </p>
      </div>
    )
  }

  const daily = stats.daily ?? []
  const byType = stats.by_library_type ?? []
  const recent = stats.recent ?? []
  const hasAnyHistory = stats.total > 0

  return (
    <div className="space-y-6">
      <PageHeader title="观看统计" description="你的观看时长、类型分布与最近记录" />

      {!hasAnyHistory ? (
        <p className="glass-panel p-8 text-center text-sm text-[var(--app-muted)]">
          还没有观看记录。播放任意内容后，这里会展示观看时长与类型分布。
        </p>
      ) : (
        <>
          <section className="grid gap-4 sm:grid-cols-3">
            <StatCard
              icon={<Clock size={16} />}
              label="累计观看"
              value={formatHours(stats.watched_hours)}
              hint={`共 ${stats.total} 条播放记录`}
            />
            <StatCard
              icon={<Star size={16} />}
              label="已看完"
              value={String(stats.completed)}
              hint={
                stats.last_watched
                  ? `最近观看 ${formatDate(stats.last_watched)}`
                  : '尚无观看时间'
              }
            />
            <StatCard
              icon={<Film size={16} />}
              label="正在看"
              value={String(stats.in_progress ?? 0)}
              hint="未标记看完的条目"
            />
          </section>

          <section className="glass-panel space-y-4">
            <div className="flex items-baseline justify-between">
              <h2 className="font-display text-lg font-semibold text-[var(--app-text)]">
                近 30 天观看时长
              </h2>
              <span className="text-xs text-[var(--app-muted)]">
                {daily.length > 0 ? `${daily.length} 天有记录` : '暂无记录'}
              </span>
            </div>
            {daily.length === 0 ? (
              <p className="py-6 text-center text-sm text-[var(--app-muted)]">
                近 30 天没有观看记录
              </p>
            ) : (
              <DailyBars daily={daily} />
            )}
          </section>

          <section className="glass-panel space-y-3">
            <h2 className="font-display text-lg font-semibold text-[var(--app-text)]">
              类型分布
            </h2>
            {byType.length === 0 ? (
              <p className="py-4 text-center text-sm text-[var(--app-muted)]">暂无数据</p>
            ) : (
              <ul className="space-y-2">
                {byType.map((row) => {
                  const max = Math.max(...byType.map((item) => item.watch_ms), 1)
                  const ratio = Math.max(2, Math.round((row.watch_ms / max) * 100))
                  return (
                    <li key={row.type} className="flex items-center gap-3">
                      <span className="flex w-20 shrink-0 items-center gap-1.5 text-xs font-semibold text-[var(--app-subtle)]">
                        {typeIcon(row.type)}
                        {typeLabel(row.type)}
                      </span>
                      <span className="h-2.5 flex-1 overflow-hidden rounded-full bg-[var(--app-hover)]">
                        <span
                          className="block h-full rounded-full bg-brand-500"
                          style={{ width: `${ratio}%` }}
                        />
                      </span>
                      <span className="w-24 shrink-0 text-right font-mono text-xs text-[var(--app-muted)]">
                        {formatHours(row.watch_ms / 1000 / 3600)} · {row.count} 条
                      </span>
                    </li>
                  )
                })}
              </ul>
            )}
          </section>

          {recent.length > 0 && (
            <section className="glass-panel space-y-3">
              <h2 className="font-display text-lg font-semibold text-[var(--app-text)]">
                最近看过
              </h2>
              <ul className="grid gap-2 sm:grid-cols-2">
                {recent.map((row) => (
                  <li key={row.history.id}>
                    <Link
                      to={`/media/${row.history.media_id}`}
                      className="flex items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-2.5 transition-colors hover:bg-[var(--app-hover)]"
                    >
                      <span className="h-14 w-10 shrink-0 overflow-hidden rounded-lg bg-[var(--app-panel-soft)]">
                        {row.media?.poster_url && (
                          <img
                            src={imageURL(
                              row.media.poster_url,
                              row.media.updated_at,
                              ARTWORK.posterTiny,
                            )}
                            alt=""
                            loading="lazy"
                            className="h-full w-full object-cover"
                          />
                        )}
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-xs font-semibold text-[var(--app-text)]">
                          {row.media?.title ?? '未知媒体'}
                        </span>
                        <span className="mt-0.5 block text-[11px] text-[var(--app-muted)]">
                          {formatDate(row.history.watched_at)}
                          {row.history.completed ? ' · 已看完' : ' · 在看'}
                        </span>
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </>
      )}
    </div>
  )
}

function StatCard({
  icon,
  label,
  value,
  hint,
}: {
  icon: React.ReactNode
  label: string
  value: string
  hint: string
}) {
  return (
    <div className="glass-panel space-y-1">
      <div className="flex items-center gap-2 text-xs font-semibold text-[var(--app-muted)]">
        {icon}
        {label}
      </div>
      <div className="font-display text-2xl font-black text-[var(--app-text)]">{value}</div>
      <div className="text-[11px] text-[var(--app-muted)]">{hint}</div>
    </div>
  )
}

// DailyBars 用纯 CSS 画 30 天柱状图。柱高按区间内最大值归一化，最小值留 4%
// 高度，否则「有记录但很少」的那天在视觉上会消失。
function DailyBars({
  daily,
}: {
  daily: Array<{ day: string; watch_ms: number; plays: number }>
}) {
  const max = Math.max(...daily.map((item) => item.watch_ms), 1)
  return (
    <div className="flex h-32 items-end gap-1">
      {daily.map((item) => {
        const ratio = Math.max(4, Math.round((item.watch_ms / max) * 100))
        return (
          <div
            key={item.day}
            className="group relative flex-1 rounded-t bg-brand-500/70 transition-colors hover:bg-brand-500"
            style={{ height: `${ratio}%` }}
            title={`${item.day} · ${formatHours(item.watch_ms / 1000 / 3600)} · ${item.plays} 条`}
          />
        )
      })}
    </div>
  )
}

function formatHours(hours: number): string {
  if (!Number.isFinite(hours) || hours <= 0) return '0 小时'
  if (hours < 1) {
    const minutes = Math.round(hours * 60)
    return `${minutes} 分钟`
  }
  return `${hours.toFixed(hours >= 10 ? 0 : 1)} 小时`
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleDateString()
}

function typeLabel(type: string): string {
  const labels: Record<string, string> = {
    movie: '电影',
    tv: '剧集',
    anime: '动漫',
    variety: '综艺',
    music: '音乐',
    adult: 'Adult',
  }
  return labels[type] ?? '其他'
}

function typeIcon(type: string) {
  if (type === 'tv' || type === 'anime' || type === 'variety') {
    return <Tv size={13} />
  }
  return <Film size={13} />
}
