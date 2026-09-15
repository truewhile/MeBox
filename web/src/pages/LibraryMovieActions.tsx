import { Database, FileText, Search, Sparkles, Trash2 } from 'lucide-react'

import type { Media } from '../types'
import { isRemoteEmbyID } from '../utils/remoteEmby'

type LibraryMovieActionsProps = {
  media: Media
  busy: boolean
  onSmartScrape: (media: Media) => void
  onManualScrape: (media: Media) => void
  onProbe: (media: Media) => void
  onNFO: (media: Media) => void
  onDelete: (media: Media) => void
}

export function LibraryMovieActions({
  media,
  busy,
  onSmartScrape,
  onManualScrape,
  onProbe,
  onNFO,
  onDelete,
}: LibraryMovieActionsProps) {
  const buttonClass = 'flex h-8 w-8 items-center justify-center rounded-lg border border-white/70 bg-white/90 text-gray-700 shadow-sm backdrop-blur transition hover:bg-brand-50 hover:text-brand-600 disabled:opacity-50'

  // 远程 Emby 条目为只读视图：刮削/探测/NFO/删除不适用。
  if (isRemoteEmbyID(media.id)) {
    return null
  }

  return (
    <div className="hidden flex-wrap justify-end gap-1 sm:flex">
      <button title="智能刮削" disabled={busy} onClick={() => onSmartScrape(media)} className={buttonClass}>
        <Sparkles size={13} />
      </button>
      <button title="手动匹配刮削" disabled={busy} onClick={() => onManualScrape(media)} className={buttonClass}>
        <Search size={13} />
      </button>
      <button title="探测媒体轨" disabled={busy} onClick={() => onProbe(media)} className={buttonClass}>
        <Database size={13} />
      </button>
      <button title="写出本地 NFO" disabled={busy} onClick={() => onNFO(media)} className={buttonClass}>
        <FileText size={13} />
      </button>
      <button title="删除" disabled={busy} onClick={() => onDelete(media)} className={`${buttonClass} hover:!bg-red-50 hover:!text-red-500`}>
        <Trash2 size={13} />
      </button>
    </div>
  )
}
