import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import toast from 'react-hot-toast'
import {
  ChevronLeft,
  ChevronRight,
  Gauge,
  ListMusic,
  Loader2,
  Pause,
  Play,
  Scissors,
  Timer,
  X,
} from 'lucide-react'
import { Virtuoso, type VirtuosoHandle } from 'react-virtuoso'

import type { ReaderChapter } from '../../api/reader'
import {
  AUDIO_CREDITS_MAX,
  AUDIO_SPEEDS,
  AUDIO_TIMERS,
  useReaderSettingsStore,
} from '../../stores/readerSettings'
import { buildChapterGroups, chapterGroupIndexOf } from '../../utils/chapterGroups'
import { readerCoverSrc } from '../../utils/readerCover'

// 音频播放面板（仿 legado AudioPlayActivity / AudioPlayService transport 行）：
// hls.js 播 m3u8，<audio> 播直链。
// transport：上一章 | -15s | 播放暂停 | +15s | 下一章；
// 动作行：章节选择、定时关闭、倍速、跳过片头片尾。
// 片头片尾按 legado 语义：全新开播（进度 0）时 seek 到 openCredits，播放到
// duration - closeCredits 即等同播完自动下一章；两者都以秒计、0 表示不跳过。

interface ReaderAudioPanelProps {
  src: string
  title: string
  /** 书籍封面：做背景（强模糊）与居中圆形封面，对应 legado ivBg / ivCover。 */
  cover: string
  theme: { bg: string; text: string; accent: string }
  initialPos: number // 恢复进度（秒）
  /** 片头跳过秒数（Book.openCredits）。 */
  openCredits: number
  /** 片尾跳过秒数（Book.closeCredits）。 */
  closeCredits: number
  chapters: ReaderChapter[]
  chapterIndex: number | null
  /** 是否还有下一章（决定片尾跳过/播完是续播还是停住）。 */
  hasNext: boolean
  /** 该音轨由服务端转码，首次播放需要等转码完成。 */
  transcoding?: boolean
  onProgress: (seconds: number) => void
  /**
   * 立即落一次进度（不节流）。只在「用户要离开这一章」时用（切后台/关页面），
   * 把节流窗口里最后几秒补上；正常播放走 onProgress 的节流上报。
   */
  onCommitProgress: (seconds: number) => void
  onPrevChapter: () => void
  onNextChapter: () => void
  onSelectChapter: (index: number) => void
  onEnded: () => void
  onCreditsChange: (open: number, close: number) => void
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

export function ReaderAudioPanel({
  src,
  title,
  cover,
  theme,
  initialPos,
  openCredits,
  closeCredits,
  chapters,
  chapterIndex,
  hasNext,
  transcoding = false,
  onProgress,
  onCommitProgress,
  onPrevChapter,
  onNextChapter,
  onSelectChapter,
  onEnded,
  onCreditsChange,
  onToggleMenu,
}: ReaderAudioPanelProps) {
  const audioRef = useRef<HTMLAudioElement>(null)
  const hlsRef = useRef<{ destroy: () => void } | null>(null)
  const restoredRef = useRef(false)
  const skippedEndRef = useRef(false)
  /** 章节抽屉的虚拟列表句柄：分组下拉靠它整段跳转。 */
  const tocRef = useRef<VirtuosoHandle>(null)

  const speed = useReaderSettingsStore((s) => s.audioSpeed)
  const setSpeed = useReaderSettingsStore((s) => s.setAudioSpeed)
  const defaultTimer = useReaderSettingsStore((s) => s.audioTimerMinutes)
  const setDefaultTimer = useReaderSettingsStore((s) => s.setAudioTimerMinutes)

  const [playing, setPlaying] = useState(false)
  const [cur, setCur] = useState(0)
  const [dur, setDur] = useState(0)
  const [loading, setLoading] = useState(true)
  // 音轨加载失败（格式不支持 / 转码失败）：界面上要给出原因，不能一直停在「加载中」
  const [audioError, setAudioError] = useState('')
  const [sheet, setSheet] = useState<Sheet>('none')
  /** 章节抽屉顶部「区间下拉」当前选中的组号，随列表滚动同步。 */
  const [groupIndex, setGroupIndex] = useState(0)
  // 当前音轨的最后播放位置（秒）。timeupdate 只在播放中触发，离开页面时要用它补报一次。
  const lastTimeRef = useRef(0)
  // onCommitProgress 每次渲染都可能换实例（父级闭包里的 chapterIndex 会变），
  // 卸载时只能通过 ref 拿到最新的那个，否则会写错章节。
  const commitRef = useRef(onCommitProgress)
  useEffect(() => {
    commitRef.current = onCommitProgress
  }, [onCommitProgress])
  // 封面可能被防盗链挡掉：加载失败就退回主题色占位，不留破图
  const [coverOK, setCoverOK] = useState(true)
  useEffect(() => {
    setCoverOK(true)
  }, [cover])
  // 明文 http 封面在 https 部署下会被混合内容策略拦掉，统一经后端代理下发
  const coverSrc = readerCoverSrc(cover)

  // 定时关闭：timerLeft 为剩余秒数，0 表示未开启（legado BaseReadAloudService.timeMinute）
  const [timerLeft, setTimerLeft] = useState(0)
  const [timerActive, setTimerActive] = useState(false)
  const [timerPreset, setTimerPreset] = useState(defaultTimer)
  const timerDeadlineRef = useRef(0)

  // 片头片尾草稿值（滑杆拖动中先本地预览，松手才落库）
  const [creditDraft, setCreditDraft] = useState({ open: openCredits, close: closeCredits })
  useEffect(() => {
    setCreditDraft({ open: openCredits, close: closeCredits })
  }, [openCredits, closeCredits])

  // ── 换源/换章：重建播放器 ──
  useEffect(() => {
    const audio = audioRef.current
    if (!audio) return
    restoredRef.current = false
    skippedEndRef.current = false
    setLoading(true)
    setAudioError('')
    setCur(0)
    setDur(0)
    // 换章时清零：否则离开页面时会把上一章的位置补报成新章节的进度
    lastTimeRef.current = 0
    let cancelled = false
    let hls: { destroy: () => void } | null = null

    const setup = async () => {
      if (src.includes('.m3u8')) {
        const mod = await import('hls.js')
        const Hls = mod.default
        if (cancelled) return
        if (Hls.isSupported()) {
          const inst = new Hls({ enableWorker: true })
          inst.loadSource(src)
          inst.attachMedia(audio)
          inst.on(Hls.Events.ERROR, (_e, data) => {
            if (data.fatal) setLoading(false)
          })
          hls = inst
          hlsRef.current = inst
        } else if (audio.canPlayType('application/vnd.apple.mpegurl')) {
          audio.src = src // Safari 原生 HLS
        }
      } else {
        audio.src = src
      }
    }
    void setup()
    return () => {
      cancelled = true
      hls?.destroy()
      hlsRef.current = null
      audio.removeAttribute('src')
      audio.load()
    }
  }, [src])

  useEffect(() => {
    const audio = audioRef.current
    if (audio) audio.playbackRate = speed
  }, [speed])

  // ── 离开这一章时补报进度 ──
  // 进度平时靠 timeupdate 节流上报（听书 5s 一次），而 timeupdate 只在播放中触发：
  // 用户按退出/切后台那一瞬间的位置没人上报，节流窗口内最后几秒就丢了。
  // 这里只处理「离开」事件：换章不会卸载本面板（src 变化时 lastTimeRef 已清零），
  // 因此不会把上一章的位置写到新章节头上。
  useEffect(() => {
    const flush = () => {
      if (lastTimeRef.current > 0) commitRef.current(lastTimeRef.current)
    }
    const onVisibilityChange = () => {
      if (document.visibilityState === 'hidden') flush()
    }
    document.addEventListener('visibilitychange', onVisibilityChange)
    window.addEventListener('pagehide', flush)
    return () => {
      document.removeEventListener('visibilitychange', onVisibilityChange)
      window.removeEventListener('pagehide', flush)
      flush()
    }
  }, [])

  // ── 定时关闭 ──
  const applyTimer = useCallback(
    (minutes: number) => {
      setDefaultTimer(minutes)
      setTimerPreset(minutes)
      if (minutes <= 0) {
        timerDeadlineRef.current = 0
        setTimerLeft(0)
        setTimerActive(false)
        return
      }
      timerDeadlineRef.current = Date.now() + minutes * 60_000
      setTimerLeft(minutes * 60)
      setTimerActive(true)
    },
    [setDefaultTimer],
  )

  // 进场套用上次的定时设置（legado 起朗读服务时 setTimer(AppConfig.ttsTimer)）
  const timerBootRef = useRef(false)
  useEffect(() => {
    if (timerBootRef.current) return
    timerBootRef.current = true
    if (defaultTimer > 0) applyTimer(defaultTimer)
  }, [applyTimer, defaultTimer])

  useEffect(() => {
    if (!timerActive) return
    const id = window.setInterval(() => {
      const audio = audioRef.current
      // 暂停期间不倒计时（legado doDs 只在播放中扣分钟），把截止时间顺延
      if (audio && audio.paused) {
        timerDeadlineRef.current += 1000
        return
      }
      const left = Math.max(0, Math.round((timerDeadlineRef.current - Date.now()) / 1000))
      setTimerLeft(left)
      if (left <= 0) {
        timerDeadlineRef.current = 0
        setTimerActive(false)
        audio?.pause()
        toast('定时结束，已暂停播放')
      }
    }, 1000)
    return () => window.clearInterval(id)
  }, [timerActive])

  // ── 播放控制 ──
  const toggle = () => {
    const audio = audioRef.current
    if (!audio) return
    if (audio.paused) void audio.play().catch(() => undefined)
    else audio.pause()
  }

  const seekBy = useCallback((delta: number) => {
    const audio = audioRef.current
    if (!audio) return
    const total = Number.isFinite(audio.duration) ? audio.duration : audio.currentTime + Math.abs(delta)
    const next = Math.min(Math.max(0, audio.currentTime + delta), Math.max(0, total - 0.2))
    audio.currentTime = next
    setCur(next)
  }, [])

  const seekTo = useCallback((seconds: number) => {
    const audio = audioRef.current
    if (!audio) return
    audio.currentTime = seconds
    setCur(seconds)
  }, [])

  const changeSpeed = (v: number) => {
    setSpeed(v)
  }

  const saveCredits = (open: number, close: number) => {
    setCreditDraft({ open, close })
    onCreditsChange(open, close)
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
    if (next === 'chapters') setGroupIndex(chapterGroupIndexOf(chapterIndex ?? 0, chapters.length))
    setSheet(next)
  }

  const chapterList = useMemo(
    () => chapters.map((c, i) => ({ ...c, i })),
    [chapters],
  )

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
            onClick={onPrevChapter}
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
            onClick={onNextChapter}
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
                    initialTopMostItemIndex={Math.max(0, chapterIndex ?? 0)}
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
                            onSelectChapter(row.i)
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

      <audio
        ref={audioRef}
        className="hidden"
        preload="metadata"
        onPlay={() => setPlaying(true)}
        onPause={() => setPlaying(false)}
        onError={(e) => {
          // 解码不了时浏览器只给错误码；这里把「能看懂的原因」摆到界面上。
          // 服务端返回的正文说明（例如未装 ffmpeg）拿不到，只能按错误码给通用解释。
          const code = e.currentTarget.error?.code
          setLoading(false)
          setAudioError(
            code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED
              ? '浏览器无法播放该音频格式，且服务端转码不可用（请检查 ffmpeg 是否已安装）'
              : code === MediaError.MEDIA_ERR_NETWORK
                ? '音频加载失败，请检查网络或源文件是否还在'
                : '音频无法播放',
          )
        }}
        onLoadedMetadata={(e) => {
          const audio = e.currentTarget
          setDur(audio.duration)
          setLoading(false)
          audio.playbackRate = speed
          if (!restoredRef.current) {
            const total = Number.isFinite(audio.duration) ? audio.duration : 0
            if (initialPos > 0 && (total === 0 || initialPos < total)) {
              // 有进度：按存档续播（legado: position != 0 时不套用片头）
              audio.currentTime = initialPos
              setCur(initialPos)
            } else if (openCredits > 0 && (total === 0 || total > openCredits + 1)) {
              // 全新开播：跳到片头结束位置（legado: skipStartMs）
              audio.currentTime = openCredits
              setCur(openCredits)
            }
          }
          restoredRef.current = true
          void audio.play().catch(() => undefined)
        }}
        onTimeUpdate={(e) => {
          const audio = e.currentTarget
          const t = audio.currentTime
          lastTimeRef.current = t
          setCur(t)
          onProgress(t)
          // 片尾跳过（legado upPlayProgress：durP >= duration - skipEnds 即当播完）
          if (
            closeCredits > 0 &&
            !skippedEndRef.current &&
            Number.isFinite(audio.duration) &&
            audio.duration > closeCredits + 1 &&
            t >= audio.duration - closeCredits
          ) {
            skippedEndRef.current = true
            if (!hasNext) {
              audio.pause()
              audio.currentTime = Math.max(0, audio.duration - 0.5)
              return
            }
            onEnded()
          }
        }}
        onEnded={onEnded}
      />
    </div>
  )
}
