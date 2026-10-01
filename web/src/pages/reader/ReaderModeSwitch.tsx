import { BookOpen, Clapperboard } from 'lucide-react'

import { useReaderSettingsStore } from '../../stores/readerSettings'

// 首页「影视 / 阅读」分段切换（样式对齐 LibraryTagBar 页签）。
export function ReaderModeSwitch() {
  const homeMode = useReaderSettingsStore((s) => s.homeMode)
  const setHomeMode = useReaderSettingsStore((s) => s.setHomeMode)

  const itemClass = (active: boolean) =>
    `flex items-center gap-1.5 rounded-xl border px-4 py-1.5 text-xs font-bold transition ${
      active
        ? 'border-brand-500/60 bg-brand-500/10 text-brand-600'
        : 'border-[var(--app-border)] text-[var(--app-muted)] hover:text-[var(--app-text)]'
    }`

  return (
    <div className="flex items-center gap-2 border-b border-[var(--app-border)] pb-3">
      <button
        type="button"
        className={itemClass(homeMode === 'media')}
        onClick={() => setHomeMode('media')}
      >
        <Clapperboard size={13} /> 影视
      </button>
      <button
        type="button"
        className={itemClass(homeMode === 'reading')}
        onClick={() => setHomeMode('reading')}
      >
        <BookOpen size={13} /> 阅读
      </button>
    </div>
  )
}
