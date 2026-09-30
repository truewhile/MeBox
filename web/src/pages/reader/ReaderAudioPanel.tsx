import { useEffect, useRef, useState } from 'react'
import { ChevronLeft, ChevronRight, Loader2, Pause, Play } from 'lucide-react'

// 音频播放面板（仿 legado ReadAloudDialog transport 行）：
// hls.js 播 m3u8，<audio> 播直链；上一章/播放暂停/下一章 + 进度条 + 倍速。

interface ReaderAudioPanelProps {
  src: string
  title: string
  theme: { bg: string; text: string; accent: string }
  initialPos: number // 恢复进度（秒）
  onProgress: (seconds: number) => void
  onPrevChapter: () => void
  onNextChapter: () => void
  onEnded: () => void
  onToggleMenu: () => void
}

const RATES = [0.75, 1, 1.25, 1.5, 2]

function fmt(sec: number): string {
  if (!Number.isFinite(sec)) return '0:00'
  const m = Math.floor(sec / 60)
  const s = Math.floor(sec % 60)
  return `${m}:${String(s).padStart(2, '0')}`
}

export function ReaderAudioPanel({
  src,
  title,
  theme,
  initialPos,
  onProgress,
  onPrevChapter,
  onNextChapter,
  onEnded,
  onToggleMenu,
}: ReaderAudioPanelProps) {
  const audioRef = useRef<HTMLAudioElement>(null)
  const hlsRef = useRef<{ destroy: () => void } | null>(null)
  const restoredRef = useRef(false)
  const [playing, setPlaying] = useState(false)
  const [cur, setCur] = useState(0)
  const [dur, setDur] = useState(0)
  const [rateIdx, setRateIdx] = useState(1)
  const [loading, setLoading] = useState(true)

  // 换源/换章：重建播放器
  useEffect(() => {
    const audio = audioRef.current
    if (!audio) return
    restoredRef.current = false
    setLoading(true)
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
    if (audio) audio.playbackRate = RATES[rateIdx]
  }, [rateIdx])

  const toggle = () => {
    const audio = audioRef.current
    if (!audio) return
    if (audio.paused) void audio.play().catch(() => undefined)
    else audio.pause()
  }

  return (
    <div className="flex h-full flex-col items-center justify-center gap-6 px-6 pb-16" onClick={onToggleMenu}>
      {/* 封面占位（居中装饰） */}
      <div
        className="flex h-28 w-28 items-center justify-center rounded-full border-2"
        style={{ borderColor: theme.accent + '66', color: theme.accent }}
      >
        <div className={playing ? 'h-16 w-16 animate-pulse rounded-full' : 'h-16 w-16 rounded-full'} style={{ backgroundColor: theme.accent + '33' }} />
      </div>
      <p className="max-w-full truncate px-4 text-sm font-bold" style={{ color: theme.text }}>
        {title}
      </p>
      <p className="text-2xs opacity-60" style={{ color: theme.text }}>
        {playing ? '播放中' : loading ? '加载中…' : '已暂停'} · 倍速 {RATES[rateIdx]}x
      </p>

      {/* 进度条 */}
      <div className="flex w-full max-w-md items-center gap-2" style={{ color: theme.text }}>
        <span className="w-10 text-right text-2xs tabular-nums opacity-70">{fmt(cur)}</span>
        <input
          type="range"
          min={0}
          max={Math.max(1, Math.floor(dur))}
          value={Math.floor(cur)}
          onChange={(e) => {
            const v = Number(e.target.value)
            setCur(v)
            if (audioRef.current) audioRef.current.currentTime = v
          }}
          className="flex-1"
          style={{ accentColor: theme.accent }}
        />
        <span className="w-10 text-2xs tabular-nums opacity-70">{fmt(dur)}</span>
      </div>

      {/* transport 行（上一章 / 播放暂停 / 下一章 / 倍速） */}
      <div className="flex items-center gap-6" style={{ color: theme.text }} onClick={(e) => e.stopPropagation()}>
        <button type="button" onClick={onPrevChapter} className="flex items-center gap-1 text-xs font-bold opacity-80 hover:opacity-100">
          <ChevronLeft size={16} /> 上一章
        </button>
        <button
          type="button"
          onClick={toggle}
          className="flex h-14 w-14 items-center justify-center rounded-full text-white shadow-lg"
          style={{ backgroundColor: theme.accent }}
          aria-label={playing ? '暂停' : '播放'}
        >
          {loading ? <Loader2 size={22} className="animate-spin" /> : playing ? <Pause size={22} /> : <Play size={22} className="ml-0.5" />}
        </button>
        <button type="button" onClick={onNextChapter} className="flex items-center gap-1 text-xs font-bold opacity-80 hover:opacity-100">
          下一章 <ChevronRight size={16} />
        </button>
      </div>
      <button
        type="button"
        onClick={() => setRateIdx((i) => (i + 1) % RATES.length)}
        className="rounded-xl border px-3 py-1 text-2xs font-bold opacity-80 hover:opacity-100"
        style={{ borderColor: theme.text + '44', color: theme.text }}
      >
        {RATES[rateIdx]}x
      </button>

      <audio
        ref={audioRef}
        className="hidden"
        onPlay={() => setPlaying(true)}
        onPause={() => setPlaying(false)}
        onLoadedMetadata={(e) => {
          setDur(e.currentTarget.duration)
          setLoading(false)
          if (!restoredRef.current && initialPos > 0 && initialPos < e.currentTarget.duration) {
            e.currentTarget.currentTime = initialPos
          }
          restoredRef.current = true
          void e.currentTarget.play().catch(() => undefined)
        }}
        onTimeUpdate={(e) => {
          setCur(e.currentTarget.currentTime)
          onProgress(e.currentTarget.currentTime)
        }}
        onEnded={onEnded}
      />
    </div>
  )
}
