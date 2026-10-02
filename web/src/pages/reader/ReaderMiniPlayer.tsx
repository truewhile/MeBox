import { useLocation, useNavigate } from 'react-router-dom'
import { Loader2, Pause, Play, X } from 'lucide-react'

import { useReaderAudioStore } from '../../stores/readerAudio'
import { readerCoverSrc } from '../../utils/readerCover'
import { getReaderAudioEngine } from './readerAudioEngine'

// 全局听书迷你条。
//
// 播放引擎是常驻单例（见 readerAudioEngine.ts），离开阅读页后音频仍在播；这个条
// 保证在其他页面也能暂停/回到阅读，否则用户会「听到声音但找不到控制」。
// 面板所在的阅读页内部不显示它（那里有完整播放面板），避免两套控制打架。
export function ReaderMiniPlayer() {
  const location = useLocation()
  const navigate = useNavigate()

  const track = useReaderAudioStore((s) => s.track)
  const bookId = useReaderAudioStore((s) => s.bookId)
  const title = useReaderAudioStore((s) => s.chapterTitle)
  const bookName = useReaderAudioStore((s) => s.bookName)
  const cover = useReaderAudioStore((s) => s.cover)
  const status = useReaderAudioStore((s) => s.status)

  const onReaderView = bookId !== '' && location.pathname === `/reader/view/${bookId}`
  if (!track || !bookId || onReaderView || status === 'idle') return null

  const engine = getReaderAudioEngine()
  const playing = status === 'playing'
  const loading = status === 'loading'
  const coverSrc = readerCoverSrc(cover)

  return (
    <div
      className="fixed inset-x-3 z-[60] flex items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)]/95 p-2 shadow-2xl backdrop-blur-md lg:inset-x-auto lg:right-6 lg:w-96"
      style={{ bottom: 'calc(env(safe-area-inset-bottom, 0px) + 3.75rem)' }}
    >
      <button
        type="button"
        onClick={() => navigate(`/reader/view/${bookId}`)}
        className="flex min-w-0 flex-1 items-center gap-2.5 text-left"
        title="回到阅读页"
      >
        <span className="h-10 w-10 shrink-0 overflow-hidden rounded-lg bg-[var(--app-brand-soft)]">
          {coverSrc ? (
            <img src={coverSrc} alt="" referrerPolicy="no-referrer" className="h-full w-full object-cover" />
          ) : null}
        </span>
        <span className="min-w-0">
          <span className="block truncate text-xs font-bold text-[var(--app-text)]">{title || bookName}</span>
          <span className="block truncate text-[10px] text-[var(--app-muted)]">{bookName || 'MeBox 听书'}</span>
        </span>
      </button>

      <button
        type="button"
        onClick={() => engine.toggle()}
        className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-brand-500 text-white shadow-sm"
        aria-label={playing ? '暂停' : '播放'}
      >
        {loading ? <Loader2 size={16} className="animate-spin" /> : playing ? <Pause size={16} /> : <Play size={16} />}
      </button>

      <button
        type="button"
        onClick={() => engine.stop()}
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-[var(--app-muted)] hover:text-[var(--app-text)]"
        aria-label="停止播放"
        title="停止播放"
      >
        <X size={16} />
      </button>
    </div>
  )
}
