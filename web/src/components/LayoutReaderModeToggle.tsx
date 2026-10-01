import { BookOpen, Clapperboard } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router-dom'

import { useReaderSettingsStore } from '../stores/readerSettings'

// 顶栏的「影视 / 阅读」切换：只显示当前模式的图标（影视 / 阅读），点一下切到另一个模块。
// 模式只作用于首页（`/`），所以不在首页时顺带跳回首页，避免点了没反应。
export function LayoutReaderModeToggle() {
  const navigate = useNavigate()
  const location = useLocation()
  const homeMode = useReaderSettingsStore((s) => s.homeMode)
  const setHomeMode = useReaderSettingsStore((s) => s.setHomeMode)

  const reading = homeMode === 'reading'
  const label = reading ? '当前是阅读模式，点击切换到影视' : '当前是影视模式，点击切换到阅读'

  const toggle = () => {
    setHomeMode(reading ? 'media' : 'reading')
    if (location.pathname !== '/') navigate('/')
  }

  return (
    <button
      type="button"
      onClick={toggle}
      title={label}
      aria-label={label}
      className={`shrink-0 rounded-xl border p-2.5 transition-colors ${
        reading
          ? 'border-brand-500/60 bg-brand-500/10 text-brand-600 hover:bg-brand-500/20'
          : 'border-[var(--app-border)] text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]'
      }`}
    >
      {reading ? <BookOpen size={18} /> : <Clapperboard size={18} />}
    </button>
  )
}
