import { useEffect, useState } from 'react'
import { ChevronDown, Dices, Filter, Loader2, X } from 'lucide-react'

import type { LibraryFacets } from '../api/library'
import {
  hasActiveFilters,
  normalizeYearRange,
  type LibraryFilterParams,
} from '../utils/libraryFilters'

type LibraryFilterBarProps = {
  facets: LibraryFacets | null
  loadingFacets: boolean
  filters: LibraryFilterParams
  onApply: (filters: LibraryFilterParams) => void
  onReset: () => void
  /** 「随便看看」：接收面板当前有效草稿（已与 URL 保持同步后）作为筛选条件。 */
  onRandom: (filters: LibraryFilterParams) => void
  randomBusy: boolean
}

// LibraryFilterBar 提供库内筛选与「随便看看」。
//
// 交互约定：面板内的改动先落到本地 draft，点「应用」才写回 URL 并重新查询。
// 类型多选是高频操作，逐个触发请求会让大库反复重扫，因此不做即时生效。
export function LibraryFilterBar({
  facets,
  loadingFacets,
  filters,
  onApply,
  onReset,
  onRandom,
  randomBusy,
}: LibraryFilterBarProps) {
  const [open, setOpen] = useState(() => hasActiveFilters(filters))
  const [draft, setDraft] = useState<LibraryFilterParams>(filters)
  const [yearMin, setYearMin] = useState(filters.year_min ? String(filters.year_min) : '')
  const [yearMax, setYearMax] = useState(filters.year_max ? String(filters.year_max) : '')
  const [ratingMin, setRatingMin] = useState(filters.rating_min ? String(filters.rating_min) : '')

  // URL 变化（例如浏览器返回）时把草稿同步回来，避免面板显示与结果不一致。
  useEffect(() => {
    setDraft(filters)
    setYearMin(filters.year_min ? String(filters.year_min) : '')
    setYearMax(filters.year_max ? String(filters.year_max) : '')
    setRatingMin(filters.rating_min ? String(filters.rating_min) : '')
  }, [filters])

  const active = hasActiveFilters(filters)

  const toggleGenre = (name: string) => {
    setDraft((prev) => {
      const exists = prev.genres.includes(name)
      return {
        ...prev,
        genres: exists ? prev.genres.filter((item) => item !== name) : [...prev.genres, name],
      }
    })
  }

  /** 把当前草稿（含年份/评分输入框）合并成一个完整的 LibraryFilterParams。 */
  const collectDraft = (): LibraryFilterParams => {
    const range = normalizeYearRange(
      yearMin ? Number(yearMin) : undefined,
      yearMax ? Number(yearMax) : undefined,
    )
    const rating = ratingMin ? Number(ratingMin) : undefined
    return {
      genres: draft.genres,
      ...range,
      rating_min: Number.isFinite(rating) && (rating ?? 0) > 0 ? rating : undefined,
      unwatched: draft.unwatched,
    }
  }

  const apply = () => {
    onApply(collectDraft())
  }

  /** 随便看看：若草稿与已应用的筛选不同，先把草稿写入 URL，再用草稿发起随机。
   *  这样「应用筛选」与「随便看看」之间的不一致窗口缩短到零。 */
  const handleRandom = () => {
    const effective = collectDraft()
    // 简单比对：序列化为 JSON 后对比，草稿改动均可检测到。
    const draftChanged = JSON.stringify(effective) !== JSON.stringify(filters)
    if (draftChanged) {
      onApply(effective)
    }
    onRandom(effective)
  }

  const reset = () => {
    setDraft({ genres: [] })
    setYearMin('')
    setYearMax('')
    setRatingMin('')
    onReset()
  }

  return (
    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)]">
      <div className="flex flex-wrap items-center justify-between gap-2 px-3 py-2.5">
        <button
          type="button"
          onClick={() => setOpen((prev) => !prev)}
          className="inline-flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]"
        >
          <Filter size={15} className="text-brand-500" />
          筛选
          {active && (
            <span className="rounded-md bg-brand-500/15 px-1.5 py-0.5 text-[10px] font-bold text-brand-500">
              已启用
            </span>
          )}
          <ChevronDown
            size={14}
            className={`text-[var(--app-muted)] transition-transform ${open ? 'rotate-180' : ''}`}
          />
        </button>

        <div className="flex items-center gap-2">
          {active && (
            <button
              type="button"
              onClick={reset}
              className="inline-flex items-center gap-1 rounded-xl border border-[var(--app-border)] px-2.5 py-1.5 text-xs font-semibold text-[var(--app-muted)] transition-colors hover:text-[var(--app-text)]"
            >
              <X size={12} />
              清除筛选
            </button>
          )}
          <button
            type="button"
            onClick={handleRandom}
            disabled={randomBusy}
            className="inline-flex items-center gap-1.5 rounded-xl border border-[var(--app-brand-border)] bg-[var(--app-brand-soft)] px-3 py-1.5 text-xs font-bold text-[var(--app-brand-text)] transition-opacity disabled:opacity-50"
            title="按当前筛选条件随机播放一条"
          >
            {randomBusy ? <Loader2 size={13} className="animate-spin" /> : <Dices size={13} />}
            随便看看
          </button>
        </div>
      </div>

      {open && (
        <div className="space-y-4 border-t border-[var(--app-border)] px-3 py-3">
          <div className="space-y-2">
            <div className="text-xs font-semibold text-[var(--app-muted)]">类型</div>
            {loadingFacets ? (
              <div className="flex items-center gap-2 py-2 text-xs text-[var(--app-muted)]">
                <Loader2 size={14} className="animate-spin" />
                正在加载类型…
              </div>
            ) : !facets || facets.genres.length === 0 ? (
              <div className="py-2 text-xs text-[var(--app-muted)]">
                这个媒体库还没有刮削出类型信息
              </div>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {facets.genres.map((genre) => {
                  const selected = draft.genres.includes(genre.name)
                  return (
                    <button
                      key={genre.name}
                      type="button"
                      onClick={() => toggleGenre(genre.name)}
                      className={
                        'rounded-lg border px-2 py-1 text-xs transition-colors ' +
                        (selected
                          ? 'border-brand-500 bg-brand-500 text-white'
                          : 'border-[var(--app-border)] text-[var(--app-subtle)] hover:bg-[var(--app-hover)]')
                      }
                    >
                      {genre.name}
                      <span className="ml-1 opacity-70">{genre.count}</span>
                    </button>
                  )
                })}
              </div>
            )}
          </div>

          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs font-semibold text-[var(--app-muted)]">
              年份
              <div className="mt-1 flex items-center gap-1.5">
                <input
                  type="number"
                  className="input-base w-20"
                  placeholder={facets?.year_min ? String(facets.year_min) : '不限'}
                  value={yearMin}
                  onChange={(e) => setYearMin(e.target.value)}
                />
                <span className="text-[var(--app-muted)]">–</span>
                <input
                  type="number"
                  className="input-base w-20"
                  placeholder={facets?.year_max ? String(facets.year_max) : '不限'}
                  value={yearMax}
                  onChange={(e) => setYearMax(e.target.value)}
                />
              </div>
            </label>

            <label className="text-xs font-semibold text-[var(--app-muted)]">
              最低评分
              <input
                type="number"
                step="0.5"
                min="0"
                max="10"
                className="input-base mt-1 w-20"
                placeholder="不限"
                value={ratingMin}
                onChange={(e) => setRatingMin(e.target.value)}
              />
            </label>

            <label className="flex items-center gap-2 pb-2 text-xs font-semibold text-[var(--app-muted)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-brand-500"
                checked={Boolean(draft.unwatched)}
                onChange={(e) => setDraft((prev) => ({ ...prev, unwatched: e.target.checked }))}
              />
              只看未看完
            </label>

            <button type="button" onClick={apply} className="neon-button mb-0.5">
              应用筛选
            </button>
          </div>
        </div>
      )}
    </section>
  )
}
