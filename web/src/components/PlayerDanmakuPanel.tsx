import { useEffect, useState } from 'react'
import { Check, ChevronRight, Film, Hash, Loader2, MessageSquareText, RefreshCw, Search, Sparkles, Tag, X } from 'lucide-react'

import type { DanmakuAnime, DanmakuEpisode, DanmakuLoadedInfo } from '../api/danmaku'

// PlayerDanmakuPanel — the on-player danmaku control panel. It displays
// the matched danmaku details (anime title, episode title, comment count,
// match mode), toggles loading, lets the user re-search by a custom keyword,
// and adjusts the renderer knobs (display area / opacity / font size) live.
type PlayerDanmakuPanelProps = {
  open: boolean
  onClose: () => void
  enabled: boolean
  onToggleEnabled: (v: boolean) => void
  search: string
  onSearch: (kw: string) => void
  searching: boolean
  area: number
  onAreaChange: (v: number) => void
  opacity: number
  onOpacityChange: (v: number) => void
  fontSize: number
  onFontSizeChange: (v: number) => void
  /** Multiple anime matched — user must pick one. */
  candidates: DanmakuAnime[]
  /** Human-readable label of the currently selected library. */
  selectedSource?: string
  /** Title used by auto-matching (e.g. anime title, media title or filename). */
  autoMatchTitle?: string
  /** Loaded danmaku metadata (title, episode, count, match mode). */
  danmakuInfo?: DanmakuLoadedInfo | null
  onSelectEpisode: (episodeId: number, animeTitle: string, episodeTitle: string) => void
  onResetAuto: () => void
}

export function PlayerDanmakuPanel({
  open,
  onClose,
  enabled,
  onToggleEnabled,
  search,
  onSearch,
  searching,
  area,
  onAreaChange,
  opacity,
  onOpacityChange,
  fontSize,
  onFontSizeChange,
  candidates,
  selectedSource,
  autoMatchTitle,
  danmakuInfo,
  onSelectEpisode,
  onResetAuto,
}: PlayerDanmakuPanelProps) {
  const [draft, setDraft] = useState(search)
  // 展开的番剧（动画 → 集数两级树），默认全展开便于选择。
  const [openAnime, setOpenAnime] = useState<Set<number>>(new Set())

  // 面板打开时同步外部搜索词到输入框草稿，并展开全部候选。
  useEffect(() => {
    if (open) {
      setDraft(search)
      setOpenAnime(new Set(candidates.map((c) => c.animeId)))
    }
  }, [open, search, candidates])

  if (!open) return null

  const toggleAnime = (animeId: number) => {
    setOpenAnime((prev) => {
      const next = new Set(prev)
      if (next.has(animeId)) {
        next.delete(animeId)
      } else {
        next.add(animeId)
      }
      return next
    })
  }

  // 匹配模式标签显示辅助
  const renderMatchBadge = (mode?: string) => {
    switch (mode) {
      case 'hash':
        return (
          <span className="inline-flex items-center gap-0.5 rounded border border-emerald-500/30 bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-medium text-emerald-300">
            <Hash size={10} /> 哈希精准匹配
          </span>
        )
      case 'filename':
        return (
          <span className="inline-flex items-center gap-0.5 rounded border border-sky-500/30 bg-sky-500/15 px-1.5 py-0.5 text-[10px] font-medium text-sky-300">
            <Tag size={10} /> 文件名匹配
          </span>
        )
      case 'search':
        return (
          <span className="inline-flex items-center gap-0.5 rounded border border-violet-500/30 bg-violet-500/15 px-1.5 py-0.5 text-[10px] font-medium text-violet-300">
            <Sparkles size={10} /> 标题搜索匹配
          </span>
        )
      case 'manual':
        return (
          <span className="inline-flex items-center gap-0.5 rounded border border-amber-500/30 bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-300">
            手动指定
          </span>
        )
      default:
        return null
    }
  }

  const isCustomOrManual = Boolean(search || selectedSource || danmakuInfo?.matchMode === 'manual')

  return (
    // 面板悬浮于视频上方：阻止点击冒泡，避免触发视频区域的播放/暂停切换。
    <div
      onClick={(e) => e.stopPropagation()}
      className="absolute inset-x-3 top-12 bottom-3 z-30 w-auto overflow-y-auto rounded-2xl border border-white/15 bg-black/85 p-4 text-white shadow-2xl backdrop-blur-md sm:inset-x-auto sm:right-4 sm:top-16 sm:bottom-auto sm:w-80"
    >
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <MessageSquareText size={16} className="text-rose-400" /> 弹幕设置
        </div>
        <button
          onClick={onClose}
          className="rounded-full p-1 text-white/60 transition hover:bg-white/10 hover:text-white"
          title="关闭"
        >
          <X size={16} />
        </button>
      </div>

      {/* 是否加载弹幕 */}
      <label className="mb-3 flex cursor-pointer items-center justify-between rounded-lg bg-white/5 px-2.5 py-2 text-sm transition hover:bg-white/10">
        <span className="text-white/85">加载弹幕</span>
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => onToggleEnabled(e.target.checked)}
          className="h-4 w-4 accent-rose-500"
        />
      </label>

      {/* 当前加载的弹幕信息卡片 */}
      {enabled && (
        <div className="mb-3">
          {searching ? (
            <div className="flex items-center justify-center gap-2 rounded-xl border border-white/10 bg-white/5 py-3 text-xs text-white/70">
              <Loader2 size={14} className="animate-spin text-rose-400" />
              <span>正在匹配弹幕…</span>
            </div>
          ) : danmakuInfo && (danmakuInfo.totalCount > 0 || danmakuInfo.animeTitle) ? (
            <div className="rounded-xl border border-white/15 bg-white/5 p-2.5">
              <div className="mb-1 flex items-start justify-between gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1 text-xs font-semibold text-white/95">
                    <Film size={13} className="shrink-0 text-rose-400" />
                    <span className="truncate" title={danmakuInfo.animeTitle || selectedSource || '未知番剧'}>
                      {danmakuInfo.animeTitle || selectedSource || '未知番剧'}
                    </span>
                  </div>
                  {danmakuInfo.episodeTitle && (
                    <div className="mt-0.5 truncate pl-4 text-[11px] text-white/60" title={danmakuInfo.episodeTitle}>
                      {danmakuInfo.episodeTitle}
                    </div>
                  )}
                </div>
                {isCustomOrManual && (
                  <button
                    onClick={onResetAuto}
                    className="flex shrink-0 items-center gap-1 rounded bg-white/10 px-1.5 py-0.5 text-[10px] text-rose-300 transition hover:bg-white/15 hover:text-rose-200"
                    title="清除手动搜索与选择，恢复自动匹配"
                  >
                    <RefreshCw size={10} />
                    自动
                  </button>
                )}
              </div>

              <div className="mt-2 flex items-center justify-between border-t border-white/10 pt-1.5 text-[11px]">
                <div>{renderMatchBadge(danmakuInfo.matchMode)}</div>
                <div className="font-mono text-white/70">
                  {danmakuInfo.totalCount > 0 ? `共 ${danmakuInfo.totalCount.toLocaleString()} 条弹幕` : '暂无弹幕内容'}
                </div>
              </div>
            </div>
          ) : candidates.length === 0 ? (
            <div className="flex items-center justify-between rounded-xl border border-white/10 bg-white/5 px-3 py-2.5 text-xs text-white/50">
              <span>未匹配到弹幕，可在下方手动搜索</span>
              {isCustomOrManual && (
                <button
                  onClick={onResetAuto}
                  className="shrink-0 text-rose-300 transition hover:text-rose-200"
                  title="恢复自动匹配"
                >
                  恢复自动
                </button>
              )}
            </div>
          ) : null}
        </div>
      )}

      {/* 搜索弹幕 */}
      <div className="mb-4">
        <div className="mb-1 flex items-center justify-between text-xs text-white/60">
          <span>搜索弹幕（留空 = 按视频名自动匹配）</span>
          {autoMatchTitle && (
            <button
              type="button"
              onClick={() => setDraft(autoMatchTitle)}
              className="text-[11px] text-rose-300 transition hover:text-rose-200"
              title="填入当前识别到的视频名"
            >
              填入当前名
            </button>
          )}
        </div>
        <div className="flex items-center gap-1.5">
          <input
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') onSearch(draft.trim())
            }}
            placeholder={autoMatchTitle ? `自动匹配：${autoMatchTitle}` : '输入番剧或电影名…'}
            className="min-w-0 flex-1 rounded-lg border border-white/15 bg-white/5 px-2.5 py-1.5 text-xs outline-none placeholder:text-white/40 focus:border-rose-400/60"
          />
          <button
            onClick={() => onSearch(draft.trim())}
            disabled={searching}
            className="flex items-center gap-1 rounded-lg bg-rose-500 px-2.5 py-1.5 text-xs font-medium text-white transition hover:bg-rose-600 disabled:opacity-50"
          >
            {searching ? <Loader2 size={13} className="animate-spin" /> : <Search size={13} />}
            搜索
          </button>
        </div>
      </div>

      {/* 多番剧命中候选列表 */}
      {candidates.length > 0 && (
        <div className="mb-4 rounded-xl border border-amber-400/25 bg-amber-400/5 p-2.5">
          <div className="mb-1.5 px-1 text-xs font-medium text-amber-200">
            搜到多部番剧，请选择对应集数：
          </div>
          <div className="max-h-52 overflow-y-auto pr-1">
            {candidates.map((anime, i) => (
              <div key={anime.animeId} className="mb-1">
                <button
                  onClick={() => toggleAnime(anime.animeId)}
                  className="flex w-full items-center gap-1 rounded-md px-1.5 py-1 text-left text-xs font-medium text-white/85 transition hover:bg-white/10"
                >
                  <ChevronRight
                    size={13}
                    className={
                      'shrink-0 transition-transform ' +
                      (openAnime.has(anime.animeId) ? 'rotate-90' : '')
                    }
                  />
                  <span className="min-w-0 flex-1 truncate">
                    {anime.animeTitle || `番剧 ${i + 1}`}
                  </span>
                  <span className="shrink-0 text-[10px] text-white/40">
                    {anime.episodes.length} 集
                  </span>
                </button>
                {openAnime.has(anime.animeId) && (
                  <div className="ml-5 border-l border-white/10 pl-2">
                    {anime.episodes.map((ep) => (
                      <EpisodeRow
                        key={ep.episodeId}
                        episode={ep}
                        onSelect={() =>
                          onSelectEpisode(ep.episodeId, anime.animeTitle, ep.episodeTitle)
                        }
                      />
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 屏幕占比（显示区域） */}
      <SliderRow
        label="屏幕占比"
        value={area}
        min={0.1}
        max={1}
        step={0.05}
        format={(v) => `${Math.round(v * 100)}%`}
        onChange={onAreaChange}
      />
      {/* 透明度 */}
      <SliderRow
        label="透明度"
        value={opacity}
        min={0.1}
        max={1}
        step={0.05}
        format={(v) => `${Math.round(v * 100)}%`}
        onChange={onOpacityChange}
      />
      {/* 字体大小 */}
      <SliderRow
        label="字体大小"
        value={fontSize}
        min={14}
        max={48}
        step={1}
        format={(v) => `${Math.round(v)}px`}
        onChange={onFontSizeChange}
      />
    </div>
  )
}

function SliderRow({
  label,
  value,
  min,
  max,
  step,
  format,
  onChange,
}: {
  label: string
  value: number
  min: number
  max: number
  step: number
  format: (v: number) => string
  onChange: (v: number) => void
}) {
  return (
    <div className="mb-2.5">
      <div className="mb-1 flex items-center justify-between text-xs">
        <span className="text-white/60">{label}</span>
        <span className="font-mono text-white/85">{format(value)}</span>
      </div>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="w-full accent-rose-500"
      />
    </div>
  )
}

function EpisodeRow({
  episode,
  onSelect,
}: {
  episode: DanmakuEpisode
  onSelect: () => void
}) {
  return (
    <button
      onClick={onSelect}
      className="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1 text-left text-xs text-white/70 transition hover:bg-rose-500/20 hover:text-white"
      title={`选择《${episode.episodeTitle}》的弹幕`}
    >
      <Check size={12} className="shrink-0 text-rose-400" />
      <span className="min-w-0 flex-1 truncate">{episode.episodeTitle}</span>
    </button>
  )
}