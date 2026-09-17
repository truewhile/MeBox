import { Cloud, Layers } from 'lucide-react'
import { Link } from 'react-router-dom'

import type { Media } from '../types'
import { isStrmMedia, mediaVersionLabel, mediaVersionsOf } from '../utils/mediaVersion'

type MediaVersionSwitcherProps = {
  media: Media
  className?: string
}

/**
 * 详情页的版本切换。
 *
 * 播放器内不再单独浮出一块版本条：多版本切换统一收在「选集」面板里
 * （见 PlayerPlaylistPanel），这里只负责详情页的同片多版本跳转。
 */
export function MediaVersionSwitcher({ media, className = '' }: MediaVersionSwitcherProps) {
  const versions = mediaVersionsOf(media)
  if (versions.length <= 1) return null

  return (
    <div className={`space-y-2 ${className}`.trim()}>
      <div className="flex items-center gap-2 text-sm font-semibold text-ink-600">
        <Layers size={14} className="text-sand-500" />
        <span>版本（{versions.length}）</span>
      </div>
      <div className="flex flex-wrap gap-2">
        {versions.map((version) => {
          const active = version.id === media.id
          const isStrm = isStrmMedia(version)
          const label = mediaVersionLabel(version)
          const btnClass = active
            ? 'border-brand-500/40 bg-brand-50 text-[#b07d35] font-bold shadow-sm'
            : 'border-gray-200 bg-white text-ink-100 hover:border-brand-500/30 hover:bg-brand-50/40'

          return (
            <Link
              key={version.id}
              to={`/play/${version.id}`}
              state={{ from: `/media/${media.id}` }}
              className={`rounded-xl border px-3 py-1.5 text-xs font-semibold transition ${btnClass}`}
              title={version.path || version.strm_url}
            >
              <span className="inline-flex items-center gap-1.5">
                {isStrm && (
                  <Cloud
                    size={12}
                    className={active ? 'text-brand-500' : 'text-sand-400'}
                    aria-label="云端 / STRM"
                  />
                )}
                <span>{label}</span>
                {active ? <span className="opacity-75">· 当前</span> : null}
              </span>
            </Link>
          )
        })}
      </div>
    </div>
  )
}
