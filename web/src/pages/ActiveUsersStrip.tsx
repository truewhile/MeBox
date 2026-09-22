import { useEffect, useState } from 'react'
import { Activity } from 'lucide-react'

import { statsAPI, type TopUserEntry } from '../api/stats'
import type { User } from '../types'

// ActiveUsersStrip 在用户管理页顶部展示播放次数最多的几个账号。
//
// 只做展示与定位，不做筛选：管理员关心的是「谁在用」，点一下滚动到该用户
// 行即可继续原来的操作。非管理员拿到 403 时静默隐藏，避免出现红色报错条。
export function ActiveUsersStrip({ users }: { users: User[] }) {
  const [top, setTop] = useState<TopUserEntry[]>([])

  useEffect(() => {
    let cancelled = false
    statsAPI
      .topUsers(5)
      .then((rows) => {
        if (!cancelled) setTop(rows)
      })
      .catch(() => {
        if (!cancelled) setTop([])
      })
    return () => {
      cancelled = true
    }
  }, [])

  if (top.length === 0) return null

  const knownIDs = new Set(users.map((u) => u.id))

  const focusUser = (userID: string) => {
    const row = document.getElementById(`admin-user-${userID}`)
    if (row) {
      row.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }
  }

  return (
    <section className="glass-panel">
      <div className="flex flex-wrap items-center gap-3">
        <span className="flex items-center gap-1.5 text-xs font-semibold text-ink-100">
          <Activity size={14} className="text-brand-500" />
          活跃用户 Top {top.length}
        </span>
        <div className="flex flex-wrap items-center gap-2">
          {top.map((entry) => {
            // 已删除的账号仍会出现在历史统计里，此时不可点击。
            const exists = knownIDs.has(entry.user_id)
            return (
              <button
                key={entry.user_id}
                type="button"
                disabled={!exists}
                onClick={() => focusUser(entry.user_id)}
                title={exists ? '定位到该用户' : '该账号已不存在'}
                className="rounded-xl border border-sand-200 bg-white px-2.5 py-1 text-[11px] font-semibold text-ink-600 transition-colors enabled:hover:border-brand-300 enabled:hover:text-brand-600 disabled:opacity-50"
              >
                {entry.username || entry.user_id}
                <span className="ml-1.5 font-mono text-[10px] text-sand-500">
                  {entry.plays} 次
                </span>
              </button>
            )
          })}
        </div>
      </div>
    </section>
  )
}
