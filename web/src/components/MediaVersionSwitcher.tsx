import { Cloud, Layers } from 'lucide-react'
import { Link } from 'react-router-dom'

import type { Media } from '../types'
import { isStrmMedia, mediaVersionLabel, mediaVersionsOf } from '../utils/mediaVersion'

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
          const isStrm = isStrmMedia(version)
          const label = mediaVersionLabel(version)
          const btnClass = dark
            ? active
              ? 'border-white/50 bg-white/20 text-white shadow-sm ring-1 ring-white/20'
              : 'border-white/15 bg-white/5 text-white/80 hover:bg-white/15 hover:text-white'
            : active
              ? 'border-brand-500/40 bg-brand-50 text-[#b07d35] font-bold shadow-sm'
              : 'border-gray-200 bg-white text-ink-100 hover:border-brand-500/30 hover:bg-brand-50/40'

          const content = (
            <span className="inline-flex items-center gap-1.5">
              {isStrm && (
                <Cloud
                  size={12}
                  className={dark ? (active ? 'text-brand-300' : 'text-white/60') : (active ? 'text-brand-500' : 'text-sand-400')}
                  aria-label="云端 / STRM"
                />
              )}
              <span>{label}</span>
              {!dark && active ? <span className="opacity-75">· 当前</span> : null}
            </span>
          )

          if (mode === 'player') {
            return (
              <button
                key={version.id}
                type="button"
                disabled={active}
                onClick={() => onSelect?.(version)}
                className={`rounded-xl border px-3 py-1.5 text-xs font-semibold transition disabled:cursor-default ${btnClass}`}
                title={version.path || version.strm_url}
              >
                {content}
              </button>
            )
          }
          return (
            <Link
              key={version.id}
              to={`/play/${version.id}`}
              state={{ from: `/media/${media.id}` }}
              className={`rounded-xl border px-3 py-1.5 text-xs font-semibold transition ${btnClass}`}
              title={version.path || version.strm_url}
            >
              {content}
            </Link>
          )
        })}
      </div>
    </div>
  )
}
