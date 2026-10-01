import { useEffect, useState } from 'react'
import { Database, HardDrive, PieChart, RefreshCw } from 'lucide-react'

import { storageAPI, type StorageBreakdown } from '../api/storage'

function fmtBytes(n: number): string {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let v = n
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(2)} ${u[i]}`
}

function fmtHours(seconds: number): string {
  if (!seconds) return '—'
  const h = Math.floor(seconds / 3600)
  return `${h.toLocaleString()} h`
}

export function LibraryStorageStats() {
  const [data, setData] = useState<StorageBreakdown | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  const loadData = () => {
    setRefreshing(true)
    storageAPI
      .breakdown()
      .then(setData)
      .finally(() => {
        setLoading(false)
        setRefreshing(false)
      })
  }

  useEffect(() => {
    loadData()
  }, [])

  if (loading) {
    return (
      <div className="glass-panel py-8 text-center text-sm text-sand-500">
        加载统计数据中…
      </div>
    )
  }

  if (!data) {
    return (
      <div className="glass-panel py-8 text-center text-sm text-sand-500">
        无法获取存储数据
      </div>
    )
  }

  const totalBytes = data.total_bytes || 1

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="font-display text-xl font-semibold text-ink-600">存储与统计</h2>
        <button
          type="button"
          onClick={loadData}
          disabled={refreshing}
          className="flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white/80 px-2.5 py-1 text-xs text-ink-100 transition hover:border-primary-400/50 hover:text-brand-500 disabled:opacity-50"
          title="刷新统计数据"
        >
          <RefreshCw size={13} className={refreshing ? 'animate-spin' : ''} />
          <span>刷新</span>
        </button>
      </div>

      <section className="grid gap-4 sm:grid-cols-3">
        <StatTile icon={<Database size={20} />} label="总占用" value={fmtBytes(data.total_bytes)} />
        <StatTile icon={<PieChart size={20} />} label="媒体库" value={`${data.by_library.length}`} />
        <StatTile icon={<HardDrive size={20} />} label="累计时长" value={fmtHours(data.total_seconds)} />
      </section>

      <section className="space-y-3">
        <h3 className="font-display text-lg font-semibold text-ink-600">按媒体库</h3>
        <div className="glass-panel table-scroll !p-3">
          <table className="min-w-[500px] w-full text-left text-sm">
            <thead className="text-xs uppercase tracking-wider text-sand-500">
              <tr>
                <th className="py-2">名称</th>
                <th>类型</th>
                <th>媒体数</th>
                <th>占用</th>
                <th>占比</th>
              </tr>
            </thead>
            <tbody>
              {data.by_library.length === 0 ? (
                <tr>
                  <td colSpan={5} className="py-4 text-center text-xs text-sand-500">
                    暂无媒体库数据
                  </td>
                </tr>
              ) : (
                data.by_library.map((l) => {
                  const pct = (l.total_bytes / totalBytes) * 100
                  return (
                    <tr key={l.library_id} className="border-t border-gray-200">
                      <td className="py-2 text-ink-600 font-medium">{l.name}</td>
                      <td className="text-ink-100">{l.type}</td>
                      <td className="text-ink-100">{l.media_count}</td>
                      <td className="text-ink-100">{fmtBytes(l.total_bytes)}</td>
                      <td>
                        <div className="flex items-center gap-2">
                          <div className="h-1.5 w-24 overflow-hidden rounded bg-gray-200">
                            <div
                              className="h-full bg-primary-400"
                              style={{ width: `${Math.min(100, Math.max(0, pct))}%` }}
                            />
                          </div>
                          <span className="text-xs text-ink-50">{pct.toFixed(1)}%</span>
                        </div>
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="space-y-3">
        <h3 className="font-display text-lg font-semibold text-ink-600">按容器格式</h3>
        {data.by_container.length === 0 ? (
          <div className="glass-panel py-4 text-center text-xs text-sand-500">
            暂无格式统计
          </div>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 md:grid-cols-3">
            {data.by_container.map((c) => (
              <div
                key={c.container}
                className="glass-panel flex items-center justify-between !p-4"
              >
                <div>
                  <p className="text-xs uppercase tracking-wider text-sand-500">{c.container || '未知'}</p>
                  <p className="font-display text-lg font-semibold text-ink-600">{c.count} 项</p>
                </div>
                <p className="text-sm text-ink-100">{fmtBytes(c.bytes)}</p>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function StatTile({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode
  label: string
  value: string
}) {
  return (
    <div className="glass-panel flex items-center gap-3 !p-4">
      <div className="rounded-xl border border-primary-400/40 bg-primary-400/10 p-2 text-brand-500">
        {icon}
      </div>
      <div>
        <p className="text-xs uppercase tracking-wider text-sand-500">{label}</p>
        <p className="font-display text-lg font-semibold text-ink-600">{value}</p>
      </div>
    </div>
  )
}
