import { memo, useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Film, Play, Layers, Star } from 'lucide-react'
import { imageURL } from '../api/client'
import type { Media } from '../types'

const ACTION_OVERLAY_CLASS =
  'absolute right-2 top-2 z-20 flex flex-wrap justify-end gap-1 opacity-100 transition-opacity sm:pointer-events-none sm:opacity-0 sm:group-hover:pointer-events-auto sm:group-hover:opacity-100 sm:focus-within:pointer-events-auto sm:focus-within:opacity-100'

// memo：父级状态变化（如轮播切图、其它卡片操作）不再级联重渲染所有卡片。
// 注意 actions/renderActions 必须引用稳定（用 renderActions 传函数）memo 才生效。
export const MediaCard = memo(function MediaCard({
  media, progress, count, rating, linkTo, onClick, actions, renderActions, compact,
}: {
  media: Media
  progress?: number
  count?: number
  rating?: number
  linkTo?: string
  onClick?: () => void
  actions?: ReactNode
  renderActions?: (media: Media) => ReactNode
  compact?: boolean
}) {
  const ref = useRef<HTMLDivElement>(null)
  const href = linkTo ?? `/media/${media.id}`
  const [posterFit, setPosterFit] = useState<'cover' | 'contain'>('cover')
  const posterSrc = imageURL(media.poster_url, media.updated_at, {
    maxWidth: compact ? 320 : 480,
    maxHeight: compact ? 480 : 600,
    quality: 82,
  })
  const blurredPosterSrc = imageURL(media.poster_url, media.updated_at, { maxWidth: 160, quality: 60 })
  const displayRating = rating ?? media.rating
  const versionCount = media.versions?.length ?? 0
  // renderActions 延迟到卡片自身渲染时才调用，保证 memo 生效
  const actionContent = actions ?? renderActions?.(media)

  useEffect(() => {
    setPosterFit('cover')
  }, [media.poster_url, media.updated_at])

  const card = (
      <div
        ref={ref}
        className="relative overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] shadow-[0_1px_3px_rgba(0,0,0,0.01),0_1px_2px_rgba(0,0,0,0.015)] transition-all duration-300 hover:scale-[1.04] hover:-translate-y-1.5 hover:border-brand-500/40 hover:shadow-[0_12px_32px_var(--app-shadow)]"
      >
        {/* Poster Wrapper */}
        <div className="relative aspect-[2/3] w-full overflow-hidden bg-[var(--app-panel-soft)]">
          {media.poster_url ? (
            <>
              {posterFit === 'contain' && (
                <img
                  src={blurredPosterSrc}
                  alt=""
                  aria-hidden="true"
                  loading="lazy"
                  className="absolute inset-0 h-full w-full scale-110 object-cover object-center opacity-25 blur-xl"
                  referrerPolicy="no-referrer"
                />
              )}
              <img
                src={posterSrc}
                alt={media.title}
                loading="lazy"
                decoding="async"
                onLoad={(event) => {
                  const img = event.currentTarget
                  setPosterFit(img.naturalWidth > img.naturalHeight ? 'contain' : 'cover')
                }}
                className={
                  'relative block h-full w-full object-center transition-transform duration-700 ease-out group-hover:scale-105 ' +
                  (posterFit === 'contain' ? 'object-contain p-1.5' : 'object-cover')
                }
                referrerPolicy="no-referrer"
              />
            </>
          ) : (
            <div className="flex h-full w-full flex-col items-center justify-center gap-2 bg-[var(--app-panel-soft)] text-[var(--app-muted)]">
              <Film size={28} className="stroke-[1.5]" />
              <span className="text-[10px] uppercase tracking-wider font-bold">No Poster</span>
            </div>
          )}

          {/* Rating Badge */}
          {displayRating > 0 && (
            <span className="absolute left-2 top-2 inline-flex items-center gap-0.5 rounded-lg border border-white/15 bg-[#111827]/90 px-1.5 py-0.5 text-[10px] font-bold text-[#c9954a] shadow-sm">
              <Star size={10} fill="currentColor" className="shrink-0" />
              <span>{displayRating.toFixed(1)}</span>
            </span>
          )}

          {/* Premium Hover Overlay（CSS 过渡替代 framer-motion 逐卡动画实例） */}
          <div className={`absolute inset-0 bg-gradient-to-t from-[#111827]/90 via-[#111827]/30 to-transparent opacity-0 group-hover:opacity-100 transition-opacity duration-300 flex flex-col justify-end ${
            compact ? 'p-3' : 'p-4'
          }`}>
            <div className="space-y-2 translate-y-4 transition-transform duration-200 group-hover:translate-y-0">
              <span className={`inline-flex items-center gap-1.5 rounded-xl bg-brand-500 px-4 py-2 font-bold text-white shadow-md shadow-brand-500/20 ${
                compact ? 'py-1.5 text-[11px]' : 'text-xs py-2'
              }`}>
                <Play size={compact ? 8 : 10} fill="currentColor" className="text-white" />
                <span>立即观影</span>
              </span>
              {!compact && (
                <p className="text-[10px] text-gray-200 font-semibold line-clamp-2 leading-relaxed">
                  {media.overview || "暂无简介内容"}
                </p>
              )}
            </div>
          </div>

          {/* Progress Bar overlay */}
          {progress !== undefined && progress > 0 && progress < 1 && (
            <div className="absolute inset-x-0 bottom-0 h-1.5 bg-[var(--app-hover)]">
              <div
                className="h-full bg-gradient-to-r from-brand-400 to-brand-500 rounded-r-full transition-all duration-300"
                style={{ width: `${Math.round(progress * 100)}%` }}
              />
            </div>
          )}
        </div>

        {/* Media Metadata Info */}
        <div className={`space-y-1 border-t border-[var(--app-border)] bg-[var(--app-panel)] ${
          compact ? 'p-2' : 'p-4'
        }`}>
          <p className={`truncate font-bold text-[var(--app-text)] transition-colors duration-200 group-hover:text-brand-500 ${
            compact ? 'text-xs' : 'text-sm'
          }`}>
            {media.title}
          </p>
          <div className="flex items-center justify-between gap-2 text-[11px] font-bold uppercase tracking-wider text-[var(--app-muted)]">
            <span>{media.year > 0 ? media.year : "未知年份"}</span>
            <span className="flex items-center gap-1.5">
              {/* 集数/版本数徽标：放在海报下方的信息行而不是压在画面上。海报底部通常
                  自带片名，角标会盖住它（Game of Thrones / Breaking Bad 都很明显）。 */}
              {(count !== undefined && count > 1) || (count === undefined && versionCount > 1) ? (
                <span className="inline-flex items-center gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-1.5 py-0.5 text-[var(--app-subtle)]">
                  <Layers size={10} className="shrink-0 text-[#c9954a]" />
                  <span>{count !== undefined && count > 1 ? `${count} 集` : `${versionCount} 版本`}</span>
                </span>
              ) : null}
              {media.video_codec && (
                <span className="rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] px-1.5 py-0.5 text-[var(--app-subtle)]">
                  {media.video_codec}
                </span>
              )}
            </span>
          </div>
        </div>
      </div>
  )

  if (onClick) {
    return (
      <div className="group relative block w-full">
        <button type="button" onClick={onClick} className="block w-full text-left">
          {card}
        </button>
        {actionContent && (
          <div className={ACTION_OVERLAY_CLASS}>
            {actionContent}
          </div>
        )}
      </div>
    )
  }

  if (actionContent) {
    return (
      <div className="group relative block">
        <Link to={href} className="block">
          {card}
        </Link>
        <div className={ACTION_OVERLAY_CLASS}>
          {actionContent}
        </div>
      </div>
    )
  }

  return (
    <Link to={href} className="group block">
        {card}
    </Link>
  )
})
