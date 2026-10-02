import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ChevronLeft, ChevronRight, Gauge, ListMusic, Loader2, Pause, Play, Scissors, Timer, X } from 'lucide-react'
import { Virtuoso, type VirtuosoHandle } from 'react-virtuoso'

import { useReaderAudioStore } from '../../stores/readerAudio'
import { AUDIO_CREDITS_MAX, AUDIO_SPEEDS, AUDIO_TIMERS } from '../../stores/readerSettings'
import { buildChapterGroups, chapterGroupIndexOf } from '../../utils/chapterGroups'
import { readerCoverSrc } from '../../utils/readerCover'
import { getReaderAudioEngine } from './readerAudioEngine'

// 音频播放面板（仿 legado AudioPlayActivity / AudioPlayService transport 行）：
// hls.js 播 m3u8，<audio> 播直链。
// transport：上一章 | -15s | 播放暂停 | +15s | 下一章；
// 动作行：章节选择、定时关闭、倍速、跳过片头片尾。
// 片头片尾按 legado 语义：全新开播（进度 0）时 seek 到 openCredits，播放到
// duration - closeCredits 即等同播完自动下一章；两者都以秒计、0 表示不跳过。
//
// 这个组件只是一层「视图」：真正的 <audio> 与全部播放状态都在常驻的
// readerAudioEngine 单例里，面板挂载/卸载都不影响播放（见该文件顶部说明）。

interface ReaderAudioPanelProps {
  theme: { bg: string; text: string; accent: string }
  onToggleMenu: () => void
}

/** -15s / +15s 步长（legado AudioPlayActivity.SEEK_STEP）。 */
const SEEK_STEP = 15
/** 片头片尾滑杆步进（秒）。 */
const CREDIT_STEP = 5
/** 动作行高度 76px：抽屉与遮罩都从动作行上沿开始，保证动作行常驻可点。 */
const ACTION_BAR_BOTTOM = 'bottom-[76px]'

type Sheet = 'none' | 'chapters' | 'timer' | 'speed' | 'credits'

function fmt(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return '0:00'
  const m = Math.floor(sec / 60)
  const s = Math.floor(sec % 60)
  return `${m}:${String(s).padStart(2, '0')}`
}

function fmtCountdown(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}:${String(s).padStart(2, '0')}`
}

export function ReaderAudioPanel({ theme, onToggleMenu }: ReaderAudioPanelProps) {
  const engine = useMemo(() => getReaderAudioEngine(), [])

  const title = useReaderAudioStore((s) => s.chapterTitle)
  const cover = useReaderAudioStore((s) => s.cover)
  const chapters = useReaderAudioStore((s) => s.chapters)
  const chapterIndex = useReaderAudioStore((s) => s.chapterIndex)
  const status = useReaderAudioStore((s) => s.status)
  const audioError = useReaderAudioStore((s) => s.error)
  const cur = useReaderAudioStore((s) => s.cur)
  const dur = useReaderAudioStore((s) => s.dur)
  const speed = useReaderAudioStore((s) => s.speed)
  const transcoding = useReaderAudioStore((s) => s.transcoding)
  const openCredits = useReaderAudioStore((s) => s.openCredits)
  const closeCredits = useReaderAudioStore((s) => s.closeCredits)
  const timerActive = useReaderAudioStore((s) => s.timerActive)
  const timerLeft = useReaderAudioStore((s) => s.timerLeft)
  const timerPreset = useReaderAudioStore((s) => s.timerPreset)

  const playing = status === 'playing'
  const loading = status === 'loading'

  const tocRef = useRef<VirtuosoHandle>(null)

  const [sheet, setSheet] = useState<Sheet>('none')
  /** 章节抽屉顶部「区间下拉」当前选中的组号，随列表滚动同步。 */
  const [groupIndex, setGroupIndex] = useState(0)

  // 封面可能被防盗链挡掉：加载失败就退回主题色占位，不留破图
  const [coverOK, setCoverOK] = useState(true)
  useEffect(() => {
    setCoverOK(true)
  }, [cover])
  // 明文 http 封面在 https 部署下会被混合内容策略拦掉，统一经后端代理下发
  const coverSrc = readerCoverSrc(cover)

  // 片头片尾草稿值（滑杆拖动中先本地预览，松手才落库）
  const [creditDraft, setCreditDraft] = useState({ open: openCredits, close: closeCredits })
  useEffect(() => {
    setCreditDraft({ open: openCredits, close: closeCredits })
  }, [openCredits, closeCredits])

  // ── 播放控制 ──
  const toggle = () => engine.toggle()
  const seekBy = useCallback((delta: number) => engine.seekBy(delta), [engine])
  const seekTo = useCallback((seconds: number) => engine.seekTo(seconds), [engine])
  const changeSpeed = (v: number) => engine.setSpeed(v)
  const applyTimer = (minutes: number) => engine.applyTimer(minutes)

  const saveCredits = (open: number, close: number) => {
    setCreditDraft({ open, close })
    engine.setCredits(open, close)
  }

  const sheetStyle = { backgroundColor: theme.bg, color: theme.text, borderColor: theme.text + '22' }

  const chipClass = (active: boolean) =>
    `rounded-lg border px-2.5 py-1 text-2xs font-bold transition ${active ? '' : 'opacity-70'}`

  const chipStyle = (active: boolean) => ({
    borderColor: active ? theme.accent : theme.text + '33',
    color: active ? theme.accent : theme.text,
    backgroundColor: active ? theme.accent + '1a' : 'transparent',
  })

  const closeSheet = () => setSheet('none')

  const openSheet = (next: Sheet) => {
    if (sheet === next) {
      setSheet('none')
      return
    }
    // 每次打开章节抽屉都把区间下拉对齐到当前章节：上次可能停在第 3 组，
    // 续读已到第 12 组，沿用旧值会显示错误的区间。
    if (next === 'chapters') setGroupIndex(chapterGroupIndexOf(chapterIndex, chapters.length))
    setSheet(next)
  }

  const chapterList = useMemo(() => chapters.map((c, i) => ({ ...c, i })), [chapters])

  // 上千章的目录整段跳转：每 100 条一组，下拉里选区间即可（见 utils/chapterGroups.ts）。
  // 目录不足一组（≤100 章）时不显示下拉——那时一屏能扫完，多了反而是噪声。
  const chapterGroups = useMemo(() => buildChapterGroups(chapterList.length), [chapterList.length])

  const jumpToChapterGroup = (next: number) => {
    const group = chapterGroups[next]
    if (!group) return
    setGroupIndex(next)
    tocRef.current?.scrollToIndex({ index: group.start, align: 'start' })
  }

  return (
    <div
      className="relative flex h-full flex-col overflow-hidden"
      onClick={() => {
        if (sheet !== 'none') closeSheet()
        else onToggleMenu()
      }}
    >
      {/* 背景：书籍封面强模糊铺满 + 主题底色蒙层（legado AudioPlayActivity.upCover 的 ivBg）。
          蒙层用主题底色但很淡：既保留日/夜主题色调，又能看清封面。 */}
      {coverSrc && coverOK && (
        <>
          <img
            src={coverSrc}
            alt=""
            aria-hidden
            referrerPolicy="no-referrer"
            className="pointer-events-none absolute inset-0 z-0 h-full w-full scale-110 object-cover"
            style={{ filter: 'blur(32px)' }}
          />
          <div
            className="pointer-events-none absolute inset-0 z-0"
            style={{ backgroundColor: theme.bg, opacity: 0.3 }}
          />
        </>
      )}

      {/* 播放信息 + transport（抽屉打开时被抽屉压住） */}
      <div className="relative z-10 flex flex-1 flex-col items-center justify-center gap-5 px-6">
        {/* 封面（legado ivCover：圆形封面图；无封面/加载失败退回主题色圆点） */}
        <div
          className="relative flex h-32 w-32 items-center justify-center rounded-full border-2"
          style={{ borderColor: theme.accent + '66', color: theme.accent }}
        >
          <div className="h-full w-full overflow-hidden rounded-full">
            {coverSrc && coverOK ? (
              <img
                src={coverSrc}
                alt={title}
                referrerPolicy="no-referrer"
                className="h-full w-full object-cover"
                onError={() => setCoverOK(false)}
              />
            ) : (
              <div className="h-full w-full" style={{ backgroundColor: theme.accent + '33' }} />
            )}
          </div>
          <span
            aria-hidden
            className={`pointer-events-none absolute inset-0 rounded-full ${playing ? 'animate-pulse' : ''}`}
            style={{ boxShadow: `0 0 0 6px ${theme.accent}22` }}
          />
        </div>

        <div className="max-w-full px-4 text-center">
          <p className="truncate text-sm font-bold" style={{ color: theme.text }}>
            {title}
          </p>
          <p className="mt-1 text-2xs opacity-60" style={{ color: theme.text }}>
            {audioError ? (
              <span className="font-bold" style={{ color: theme.accent }}>
                {audioError}
              </span>
            ) : (
              <>
                {loading ? (transcoding ? '服务端转码中，请稍候…' : '加载中…') : playing ? '播放中' : '已暂停'} ·
                倍速 {speed}x
              </>
            )}
            {!audioError && timerActive ? ` · 定时 ${fmtCountdown(timerLeft)}` : ''}
            {!audioError && (openCredits > 0 || closeCredits > 0)
              ? ` · 跳过片头${openCredits}s/片尾${closeCredits}s`
              : ''}
          </p>
        </div>

        {/* 进度条 */}
        <div className="flex w-full max-w-md items-center gap-2" style={{ color: theme.text }}>
          <span className="w-10 text-right text-2xs tabular-nums opacity-70">{fmt(cur)}</span>
          <input
            type="range"
            min={0}
            max={Math.max(1, Math.floor(dur))}
            value={Math.floor(cur)}
            onChange={(e) => seekTo(Number(e.target.value))}
            onClick={(e) => e.stopPropagation()}
            className="flex-1"
            style={{ accentColor: theme.accent }}
          />
          <span className="w-10 text-2xs tabular-nums opacity-70">{fmt(dur)}</span>
        </div>

        {/* transport 行：上一章 | -15s | 播放暂停 | +15s | 下一章 */}
        <div
          className="flex items-center gap-5"
          style={{ color: theme.text }}
          onClick={(e) => e.stopPropagation()}
        >
          <button
            type="button"
            onClick={() => engine.prev()}
            className="flex flex-col items-center gap-0.5 opacity-80 hover:opacity-100"
            aria-label="上一章"
            title="上一章"
          >
            <ChevronLeft size={22} />
          </button>
          <button
            type="button"
            onClick={() => seekBy(-SEEK_STEP)}
            className="flex h-10 w-10 items-center justify-center rounded-full border text-2xs font-bold opacity-80 hover:opacity-100"
            style={{ borderColor: theme.text + '44' }}
            aria-label={`后退 ${SEEK_STEP} 秒`}
            title={`后退 ${SEEK_STEP} 秒`}
          >
            <ChevronLeft size={12} />
            <span className="text-[10px]">{SEEK_STEP}s</span>
          </button>
          <button
            type="button"
            onClick={toggle}
            className="flex h-14 w-14 items-center justify-center rounded-full text-white shadow-lg"
            style={{ backgroundColor: theme.accent }}
            aria-label={playing ? '暂停' : '播放'}
          >
            {loading ? (
              <Loader2 size={22} className="animate-spin" />
            ) : playing ? (
              <Pause size={22} />
            ) : (
              <Play size={22} className="ml-0.5" />
            )}
          </button>
          <button
            type="button"
            onClick={() => seekBy(SEEK_STEP)}
            className="flex h-10 w-10 items-center justify-center rounded-full border text-2xs font-bold opacity-80 hover:opacity-100"
            style={{ borderColor: theme.text + '44' }}
            aria-label={`前进 ${SEEK_STEP} 秒`}
            title={`前进 ${SEEK_STEP} 秒`}
          >
            <ChevronRight size={12} />
            <span className="text-[10px]">{SEEK_STEP}s</span>
          </button>
          <button
            type="button"
            onClick={() => engine.next()}
            className="flex flex-col items-center gap-0.5 opacity-80 hover:opacity-100"
            aria-label="下一章"
            title="下一章"
          >
            <ChevronRight size={22} />
          </button>
        </div>
      </div>

      {/* 动作行：常驻底部，抽屉打开时仍可点（可在几个抽屉间直接切换） */}
      <div
        className="relative z-50 grid h-[76px] w-full shrink-0 grid-cols-4 px-6"
        style={{ color: theme.text }}
        onClick={(e) => e.stopPropagation()}
      >
        {(
          [
            {
              icon: <ListMusic size={18} />,
              label: '章节',
              active: sheet === 'chapters',
              action: () => openSheet('chapters'),
            },
            {
              icon: <Timer size={18} />,
              label: timerActive ? fmtCountdown(timerLeft) : '定时',
              active: timerActive || sheet === 'timer',
              action: () => openSheet('timer'),
            },
            {
              icon: <Gauge size={18} />,
              label: `${speed}x`,
              active: speed !== 1 || sheet === 'speed',
              action: () => openSheet('speed'),
            },
            {
              icon: <Scissors size={18} />,
              label: '片头片尾',
              active: openCredits > 0 || closeCredits > 0 || sheet === 'credits',
              action: () => openSheet('credits'),
            },
          ] as const
        ).map((item) => (
          <button
            key={item.label}
            type="button"
            onClick={item.action}
            className="flex flex-col items-center justify-center gap-1 opacity-80 hover:opacity-100"
            style={item.active ? { color: theme.accent, opacity: 1 } : undefined}
          >
            {item.icon}
            <span className="text-2xs tabular-nums">{item.label}</span>
          </button>
        ))}
      </div>

      {/* ── 底部抽屉（到动作行上沿为止，动作行始终可点） ── */}
      {sheet !== 'none' && (
        <>
          <button
            type="button"
            aria-label="关闭面板"
            className={`absolute inset-x-0 top-0 z-30 cursor-default bg-black/30 ${ACTION_BAR_BOTTOM}`}
            onClick={closeSheet}
          />
          <div
            className={`absolute inset-x-0 z-40 max-h-[65%] overflow-hidden rounded-t-2xl border-t ${ACTION_BAR_BOTTOM}`}
            style={sheetStyle}
            onClick={(e) => e.stopPropagation()}
          >
            {sheet === 'chapters' && (
              <div className="flex h-full flex-col">
                <div
                  className="flex items-center gap-2 border-b px-4 py-3 text-xs font-bold"
                  style={{ borderColor: theme.text + '22' }}
                >
                  <span className="shrink-0">章节（{chapters.length}）</span>
                  {chapterGroups.length > 1 && (
                    <select
                      value={groupIndex}
                      onChange={(e) => jumpToChapterGroup(Number(e.target.value))}
                      aria-label="按区间快速定位章节"
                      title="按区间快速定位章节"
                      className="min-w-0 flex-1 rounded-lg border bg-transparent px-2 py-1 text-2xs font-normal outline-none"
                      style={{ borderColor: theme.text + '33', color: theme.text }}
                    >
                      {chapterGroups.map((group) => (
                        <option
                          key={group.index}
                          value={group.index}
                          style={{ color: '#111827', backgroundColor: '#ffffff' }}
                        >
                          {group.label}
                        </option>
                      ))}
                    </select>
                  )}
                  <button
                    type="button"
                    onClick={closeSheet}
                    className="shrink-0 opacity-70 hover:opacity-100"
                  >
                    <X size={16} />
                  </button>
                </div>
                <div className="h-[45vh] min-h-0">
                  <Virtuoso
                    ref={tocRef}
                    data={chapterList}
                    initialTopMostItemIndex={Math.max(0, chapterIndex)}
                    rangeChanged={(range) =>
                      setGroupIndex(chapterGroupIndexOf(range.startIndex, chapterList.length))
                    }
                    itemContent={(_i, row) => {
                      const isCurrent = row.i === chapterIndex
                      return (
                        <button
                          type="button"
                          onClick={() => {
                            if (row.is_volume) return
                            closeSheet()
                            engine.selectChapter(row.i)
                          }}
                          className={`block w-full truncate px-4 py-2.5 text-left text-xs ${
                            row.is_volume ? 'font-bold opacity-70' : ''
                          }`}
                          style={isCurrent ? { color: theme.accent, fontWeight: 700 } : undefined}
                        >
                          {row.title}
                        </button>
                      )
                    }}
                  />
                </div>
              </div>
            )}

            {sheet === 'timer' && (
              <div className="px-4 pb-6 pt-4">
                <p className="mb-3 text-xs font-bold">定时关闭</p>
                <div className="flex flex-wrap gap-2">
                  {AUDIO_TIMERS.map((m) => {
                    const active = timerActive ? timerPreset === m : m === 0
                    return (
                      <button
                        key={m}
                        type="button"
                        onClick={() => applyTimer(m)}
                        className={chipClass(active)}
                        style={chipStyle(active)}
                      >
                        {m === 0 ? '关闭定时' : `${m} 分钟`}
                      </button>
                    )
                  })}
                </div>
                <p className="mt-3 text-2xs opacity-60">
                  {timerActive ? `剩余 ${fmtCountdown(timerLeft)}，播完自动暂停` : '未开启：不会自动停止播放'}
                </p>
              </div>
            )}

            {sheet === 'speed' && (
              <div className="px-4 pb-6 pt-4">
                <div className="mb-3 flex items-center justify-between text-xs font-bold">
                  <span>播放倍速</span>
                  <span className="tabular-nums" style={{ color: theme.accent }}>
                    {speed}x
                  </span>
                </div>
                <input
                  type="range"
                  min={0.5}
                  max={3}
                  step={0.1}
                  value={speed}
                  onChange={(e) => changeSpeed(Number(e.target.value))}
                  className="w-full"
                  style={{ accentColor: theme.accent }}
                />
                <div className="mt-3 flex flex-wrap gap-2">
                  {AUDIO_SPEEDS.map((v) => (
                    <button
                      key={v}
                      type="button"
                      onClick={() => changeSpeed(v)}
                      className={chipClass(v === speed)}
                      style={chipStyle(v === speed)}
                    >
                      {v}x
                    </button>
                  ))}
                </div>
              </div>
            )}

            {sheet === 'credits' && (
              <div className="px-4 pb-6 pt-4">
                <div className="mb-3 flex items-center justify-between text-xs font-bold">
                  <span>跳过片头片尾</span>
                  <button
                    type="button"
                    onClick={() => saveCredits(0, 0)}
                    className="text-2xs opacity-70 hover:opacity-100"
                  >
                    重置
                  </button>
                </div>
                <div className="space-y-4">
                  <div>
                    <div className="mb-1 flex items-center justify-between text-2xs">
                      <span className="opacity-70">片头</span>
                      <span className="tabular-nums" style={{ color: theme.accent }}>
                        {creditDraft.open} 秒
                      </span>
                    </div>
                    <input
                      type="range"
                      min={0}
                      max={AUDIO_CREDITS_MAX}
                      step={CREDIT_STEP}
                      value={creditDraft.open}
                      onChange={(e) => setCreditDraft((d) => ({ ...d, open: Number(e.target.value) }))}
                      onPointerUp={() => saveCredits(creditDraft.open, creditDraft.close)}
                      onKeyUp={() => saveCredits(creditDraft.open, creditDraft.close)}
                      className="w-full"
                      style={{ accentColor: theme.accent }}
                    />
                  </div>
                  <div>
                    <div className="mb-1 flex items-center justify-between text-2xs">
                      <span className="opacity-70">片尾</span>
                      <span className="tabular-nums" style={{ color: theme.accent }}>
                        {creditDraft.close} 秒
                      </span>
                    </div>
                    <input
                      type="range"
                      min={0}
                      max={AUDIO_CREDITS_MAX}
                      step={CREDIT_STEP}
                      value={creditDraft.close}
                      onChange={(e) => setCreditDraft((d) => ({ ...d, close: Number(e.target.value) }))}
                      onPointerUp={() => saveCredits(creditDraft.open, creditDraft.close)}
                      onKeyUp={() => saveCredits(creditDraft.open, creditDraft.close)}
                      className="w-full"
                      style={{ accentColor: theme.accent }}
                    />
                  </div>
                </div>
                <div className="mt-3 flex flex-wrap gap-2">
                  {[0, 15, 30, 45, 60, 90].map((v) => (
                    <button
                      key={v}
                      type="button"
                      onClick={() => saveCredits(v, creditDraft.close)}
                      className={chipClass(v === creditDraft.open)}
                      style={chipStyle(v === creditDraft.open)}
                    >
                      片头 {v}s
                    </button>
                  ))}
                </div>
                <p className="mt-3 text-2xs opacity-60">
                  每章从头播放时跳过前 {creditDraft.open} 秒；剩最后 {creditDraft.close} 秒时视为本章播完。
                </p>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}
