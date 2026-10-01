import { useEffect, useState } from 'react'
import { BookOpen } from 'lucide-react'

import { isRenderableCover } from '../utils/coverUrl'

// 阅读子系统统一封面图。
//
// 书源返回的 cover_url 经常不是图片（空值、站点地址、data: 参数信封），
// 直接塞进 <img> 会显示破图。这里统一处理：地址不是图片、或加载失败时，
// 一律回退成占位图标。
export default function ReaderBookCover({
  url,
  alt = '',
  iconSize = 18,
  className = 'h-full w-full object-cover',
}: {
  url?: string | null
  alt?: string
  iconSize?: number
  className?: string
}) {
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    setFailed(false)
  }, [url])

  if (!isRenderableCover(url) || failed) {
    return (
      <div className="flex h-full w-full items-center justify-center">
        <BookOpen size={iconSize} className="text-[var(--app-muted)]" />
      </div>
    )
  }

  return (
    <img
      src={url ?? ''}
      alt={alt}
      loading="lazy"
      referrerPolicy="no-referrer"
      className={className}
      onError={() => setFailed(true)}
    />
  )
}
