import { useEffect, useState } from 'react'
import { Check, ChevronDown, ChevronRight, Eye, EyeOff, Film, Hash, KeyRound, Loader2, MessageSquareText, RefreshCw, Search, Server, Settings2, Sparkles, Tag, X } from 'lucide-react'

import type { DanmakuAnime, DanmakuEpisode, DanmakuLoadedInfo } from '../api/danmaku'
import { PLAYER_DRAWER, PLAYER_ICON_BUTTON, PLAYER_PANEL_HEADER } from './playerTheme'

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
  onAreaCommit: (v: number) => void
  opacity: number
  onOpacityChange: (v: number) => void
  onOpacityCommit: (v: number) => void
  fontSize: number
  onFontSizeChange: (v: number) => void
  onFontSizeCommit: (v: number) => void
  /** Per-user danmaku service endpoint and optional application credentials. */
  source: string
  appId: string
  appKeyConfigured: boolean
  settingsSaving?: boolean
  onSaveAdvanced: (values: {
    source: string
    appId: string
    appKey: string
    clearAppKey: boolean
  }) => Promise<void>
  /** Multiple anime matched — user must pick one. */
  candidates: DanmakuAnime[]
  /**
   * Other libraries holding this same episode. Danmaku is already loaded from
   * one of them; these exist so the user can switch sources on the spot.
   */
  alternatives: DanmakuAnime[]
  /** Per-user preference: merge the same episode's sources into one list. */
  mergeSources: boolean
  onMergeSourcesChange: (v: boolean) => void
  /** True while the merge preference is being persisted. */
  mergeSaving?: boolean
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
  onAreaCommit,
  opacity,
  onOpacityChange,
  onOpacityCommit,
  fontSize,
  onFontSizeChange,
  onFontSizeCommit,
  source,
  appId,
  appKeyConfigured,
  settingsSaving = false,
  onSaveAdvanced,
  candidates,
  alternatives,
  mergeSources,
  onMergeSourcesChange,
  mergeSaving = false,
  selectedSource,
  autoMatchTitle,
  danmakuInfo,
  onSelectEpisode,
  onResetAuto,
}: PlayerDanmakuPanelProps) {
  const [draft, setDraft] = useState(search)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [sourceDraft, setSourceDraft] = useState(source)
  const [appIdDraft, setAppIdDraft] = useState(appId)
  const [appKeyDraft, setAppKeyDraft] = useState('')
  const [showAppKey, setShowAppKey] = useState(false)
  const [clearAppKey, setClearAppKey] = useState(false)
  const [advancedSaving, setAdvancedSaving] = useState(false)
  // 展开的番剧（动画 → 集数两级树），默认全展开便于选择。
  const [openAnime, setOpenAnime] = useState<Set<number>>(new Set())

  // 面板打开时同步外部搜索词到输入框草稿，并展开全部候选。
  useEffect(() => {
    if (open) {
      setDraft(search)
      setOpenAnime(new Set(candidates.map((c) => c.animeId)))
      setSourceDraft(source)
      setAppIdDraft(appId)
      setAppKeyDraft('')
      setShowAppKey(false)
      setClearAppKey(false)
    }
  }, [open, search, candidates, source, appId])

  const saveAdvanced = async () => {
    setAdvancedSaving(true)
    try {
      await onSaveAdvanced({
        source: sourceDraft,
        appId: appIdDraft,
        appKey: appKeyDraft,
        clearAppKey,
      })
      setAppKeyDraft('')
      setShowAppKey(false)
      setClearAppKey(false)
    } catch {
      // 父组件已展示错误提示，这里保持面板打开以便修正后重试。
    } finally {
      setAdvancedSaving(false)
    }
  }

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
      case 'metadata':
        return (
          <span className="inline-flex items-center gap-0.5 rounded border border-cyan-500/30 bg-cyan-500/15 px-1.5 py-0.5 text-[10px] font-medium text-cyan-300">
            <Search size={10} /> 刮削信息匹配
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

  // 候选来源摊平成「番剧 + 集」的一维列表：每个来源通常只含命中的那一集。
  const alternativeRows = alternatives.flatMap((anime) =>
    anime.episodes.map((ep) => ({ anime, ep })),
  )

  return (
    // 面板悬浮于视频上方：阻止点击冒泡，避免触发视频区域的播放/暂停切换。
    // 排布和选集抽屉完全一致（整条贴住右边缘、标题栏常驻），两个面板互斥打开时
    // 位置不会跳，用户也不用重新找入口。
    <div
      onClick={(e) => e.stopPropagation()}
      onPointerDown={(e) => e.stopPropagation()}
      onPointerUp={(e) => e.stopPropagation()}
      className={`absolute inset-y-0 right-0 z-30 ${PLAYER_DRAWER}`}
    >
      <div className={PLAYER_PANEL_HEADER}>
        <div className="flex min-w-0 items-center gap-2 text-sm font-semibold">
          <MessageSquareText size={16} className="shrink-0 text-rose-400" />
          <span className="truncate">弹幕设置</span>
          {settingsSaving && <Loader2 size={12} className="shrink-0 animate-spin text-rose-300" />}
        </div>
        <button onClick={onClose} className={PLAYER_ICON_BUTTON} title="关闭 (Esc)">
          <X size={16} />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 py-3.5">
      {/* 弹幕总开关。从操作栏的「弹」按钮直接进来就到了这里，所以这一项就是
          用户找的「开关弹幕」；文案跟旧的按钮提示保持一致，避免换个说法让人找不到。 */}
      <label className="mb-3 flex cursor-pointer items-center justify-between rounded-lg bg-white/5 px-2.5 py-2 text-sm transition hover:bg-white/10">
        <span className="text-white/85">显示弹幕</span>
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

      {/* 同集其它来源：弹幕已自动加载，这里直接切换即可 */}
      {enabled && alternatives.length > 0 && (
        <div className="mb-4 rounded-xl border border-sky-400/25 bg-sky-400/5 p-2.5">
          <div className="mb-1.5 px-1 text-xs font-medium text-sky-200">
            同集其它来源（{alternativeRows.length}）
          </div>
          <div className="mb-1.5 px-1 text-[10px] leading-relaxed text-white/45">
            当前已自动加载一个来源，点其它条目可直接切换，无需重新搜索。
          </div>
          <div className="max-h-52 overflow-y-auto pr-1">
            {alternativeRows.map(({ anime, ep }, i) => {
              const current = String(danmakuInfo?.episodeId ?? '') === String(ep.episodeId)
              return (
                <button
                  key={`${anime.animeId}-${ep.episodeId}-${i}`}
                  onClick={() => onSelectEpisode(ep.episodeId, anime.animeTitle, ep.episodeTitle)}
                  className={
                    'mb-1 flex w-full items-start gap-1.5 rounded-md px-1.5 py-1.5 text-left text-xs transition ' +
                    (current
                      ? 'bg-sky-500/20 text-white'
                      : 'text-white/70 hover:bg-sky-500/15 hover:text-white')
                  }
                  title={`切换到《${anime.animeTitle}》的《${ep.episodeTitle}》`}
                >
                  <span className="mt-0.5 shrink-0">
                    {current ? (
                      <Check size={12} className="text-sky-300" />
                    ) : (
                      <Film size={12} className="text-white/35" />
                    )}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{anime.animeTitle || `来源 ${i + 1}`}</span>
                    {ep.episodeTitle && (
                      <span className="mt-0.5 block truncate text-[10px] text-white/50">
                        {ep.episodeTitle}
                      </span>
                    )}
                  </span>
                  {current && (
                    <span className="mt-0.5 shrink-0 text-[10px] text-sky-300">当前</span>
                  )}
                </button>
              )
            })}
          </div>
        </div>
      )}

      {/* 合并多来源：仅在确实存在多个同集来源时才出现，避免无意义的开关 */}
      {enabled && alternativeRows.length > 1 && (
        <div className="mb-4 rounded-xl border border-emerald-400/25 bg-emerald-400/5 p-2.5">
          <label className="flex cursor-pointer items-start justify-between gap-2">
            <span className="min-w-0">
              <span className="block text-xs font-medium text-emerald-200">合并多来源弹幕</span>
              <span className="mt-0.5 block text-[10px] leading-relaxed text-white/45">
                把以上来源的弹幕合并，并按时间与内容去重后一起显示。该设置会保存到账号。
              </span>
            </span>
            <span className="flex shrink-0 items-center gap-1.5 pt-0.5">
              {mergeSaving && <Loader2 size={11} className="animate-spin text-emerald-300" />}
              <input
                type="checkbox"
                checked={mergeSources}
                onChange={(e) => onMergeSourcesChange(e.target.checked)}
                className="h-4 w-4 accent-emerald-500"
              />
            </span>
          </label>
          {mergeSources && danmakuInfo?.mergedSources ? (
            <div className="mt-1.5 border-t border-white/10 pt-1.5 text-[10px] text-emerald-300/80">
              已合并 {danmakuInfo.mergedSources} 个来源
            </div>
          ) : null}
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
        onCommit={onAreaCommit}
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
        onCommit={onOpacityCommit}
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
        onCommit={onFontSizeCommit}
      />
      <div className="mt-3 border-t border-white/10 pt-3">
        <button
          type="button"
          onClick={() => setAdvancedOpen((value) => !value)}
          className="flex w-full items-center justify-between rounded-lg bg-white/5 px-2.5 py-2 text-xs font-medium text-white/75 transition hover:bg-white/10 hover:text-white"
        >
          <span className="flex items-center gap-1.5">
            <Settings2 size={13} className="text-rose-300" /> 高级
          </span>
          <ChevronDown
            size={14}
            className={'transition-transform ' + (advancedOpen ? 'rotate-180' : '')}
          />
        </button>

        {advancedOpen && (
          <div className="mt-2 space-y-2.5 rounded-xl border border-white/10 bg-black/30 p-2.5">
            <label className="block">
              <span className="mb-1 flex items-center gap-1 text-[11px] text-white/55">
                <Server size={11} /> 弹幕服务地址
              </span>
              <input
                value={sourceDraft}
                onChange={(e) => setSourceDraft(e.target.value)}
                placeholder="留空使用 https://api.dandanplay.net"
                className="w-full rounded-lg border border-white/15 bg-white/5 px-2.5 py-2 text-xs outline-none placeholder:text-white/30 focus:border-rose-400/60"
              />
            </label>

            <label className="block">
              <span className="mb-1 flex items-center gap-1 text-[11px] text-white/55">
                <KeyRound size={11} /> 开放 API AppId
              </span>
              <input
                value={appIdDraft}
                onChange={(e) => setAppIdDraft(e.target.value)}
                placeholder="官方源可留空；第三方源按需填写"
                className="w-full rounded-lg border border-white/15 bg-white/5 px-2.5 py-2 text-xs outline-none placeholder:text-white/30 focus:border-rose-400/60"
              />
            </label>

            <label className="block">
              <span className="mb-1 flex items-center justify-between gap-2 text-[11px] text-white/55">
                <span className="flex items-center gap-1">
                  <KeyRound size={11} /> 应用密钥 AppSecret
                </span>
                {appKeyConfigured && !clearAppKey && (
                  <span className="text-[10px] text-emerald-300">已保存</span>
                )}
              </span>
              <span className="relative block">
                <input
                  type={showAppKey ? 'text' : 'password'}
                  value={appKeyDraft}
                  disabled={clearAppKey}
                  onChange={(e) => setAppKeyDraft(e.target.value)}
                  placeholder={appKeyConfigured ? '留空保持不变' : '仅保存在服务端'}
                  className="w-full rounded-lg border border-white/15 bg-white/5 px-2.5 py-2 pr-8 text-xs outline-none placeholder:text-white/30 focus:border-rose-400/60 disabled:opacity-40"
                />
                <button
                  type="button"
                  onClick={() => setShowAppKey((value) => !value)}
                  disabled={clearAppKey}
                  className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-1 text-white/45 hover:text-white disabled:opacity-30"
                  title={showAppKey ? '隐藏密钥' : '显示密钥'}
                >
                  {showAppKey ? <EyeOff size={12} /> : <Eye size={12} />}
                </button>
              </span>
            </label>

            {appKeyConfigured && (
              <label className="flex cursor-pointer items-center gap-2 text-[10px] text-amber-200/75">
                <input
                  type="checkbox"
                  checked={clearAppKey}
                  onChange={(e) => setClearAppKey(e.target.checked)}
                  className="h-3.5 w-3.5 accent-amber-500"
                />
                清除已保存的应用密钥
              </label>
            )}

            <button
              type="button"
              onClick={() => void saveAdvanced()}
              disabled={advancedSaving || settingsSaving}
              className="flex w-full items-center justify-center gap-1.5 rounded-lg bg-rose-500 px-2.5 py-2 text-xs font-medium text-white transition hover:bg-rose-600 disabled:opacity-50"
            >
              {(advancedSaving || settingsSaving) && <Loader2 size={12} className="animate-spin" />}
              保存服务设置
            </button>
            <p className="text-[10px] leading-relaxed text-white/35">
              密钥不会回传到浏览器，留空时保持数据库中的原值；官方源留空凭据会使用内置回退，自定义源需自行提供。修改后仅对当前账号生效。
            </p>
          </div>
        )}
      </div>
      </div>
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
  onCommit,
}: {
  label: string
  value: number
  min: number
  max: number
  step: number
  format: (v: number) => string
  onChange: (v: number) => void
  onCommit: (v: number) => void
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
        onPointerUp={(e) => onCommit(Number(e.currentTarget.value))}
        onKeyUp={(e) => onCommit(Number(e.currentTarget.value))}
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
