import { Layers } from 'lucide-react'
import { Link } from 'react-router-dom'

import type { Media } from '../types'
import { mediaVersionLabel, mediaVersionsOf } from '../utils/mediaVersion'

type MediaVersionSwitcherProps = {
  media: Media
  /** 详情页：用 Link 跳转播放；播放页：回调切换 */
  mode?: 'detail' | 'player'
  onSelect?: (version: Media) => void
  className?: string
}

export function MediaVersionSwitcher({
  media,
  mode = 'detail',
  onSelect,
  className = '',
}: MediaVersionSwitcherProps) {
  const versions = mediaVersionsOf(media)
  if (versions.length <= 1) return null
  const dark = mode === 'player'

  return (
    <div className={`space-y-2 ${className}`.trim()}>
      <div className={`flex items-center gap-2 text-sm font-semibold ${dark ? 'text-white/90' : 'text-ink-600'}`}>
        <Layers size={14} className={dark ? 'text-white/60' : 'text-sand-500'} />
        <span>版本（{versions.length}）</span>
      </div>
      <div className="flex flex-wrap gap-2">
        {versions.map((version) => {
          const active = version.id === media.id
          const label = mediaVersionLabel(version)
          const className = dark
            ? active
              ? 'border-white/40 bg-white/20 text-white'
              : 'border-white/15 bg-white/5 text-white/80 hover:bg-white/15'
            : active
              ? 'border-brand-500/40 bg-brand-50 text-[#b07d35]'
              : 'border-gray-200 bg-white text-ink-100 hover:border-brand-500/30 hover:bg-brand-50/40'
          if (mode === 'player') {
            return (
              <button
                key={version.id}
                type="button"
                disabled={active}
                onClick={() => onSelect?.(version)}
                className={`rounded-xl border px-3 py-1.5 text-xs font-semibold transition disabled:cursor-default ${className}`}
                title={version.path}
              >
                {label}
              </button>
            )
          }
          return (
            <Link
              key={version.id}
              to={`/play/${version.id}`}
              state={{ from: `/media/${media.id}` }}
              className={`rounded-xl border px-3 py-1.5 text-xs font-semibold transition ${className}`}
              title={version.path}
            >
              {label}
              {active ? ' · 当前' : ''}
            </Link>
          )
        })}
      </div>
    </div>
  )
}
