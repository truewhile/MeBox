import { useEffect, useState, type SyntheticEvent } from 'react'
import { BookOpen } from 'lucide-react'

import { isRenderableCover } from '../utils/coverUrl'
import { readerCoverSrc } from '../utils/readerCover'

// 阅读子系统统一封面图。
//
// 书源返回的 cover_url 经常不是图片（空值、站点地址、data: 参数信封），
// 直接塞进 <img> 会显示破图。这里统一处理：地址不是图片、或加载失败时，
// 一律回退成占位图标。
//
// 另外，明文 http 的封面在 https 部署下会被混合内容策略拦掉，统一经后端图片
// 代理下发（见 readerCoverSrc），否则这些封面会全部退化成占位图标。
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

  const src = readerCoverSrc(url)

  // 后端图片代理抓不到上游时会回一张 1×1 透明 PNG（HTTP 200），<img> 因此不会
  // 触发 onError。书源在「没有封面」时可能返回源站首页这类非图片地址，代理同样
  // 只能给出这张占位像素——按加载失败处理，才会退回图标而不是留下一块空白。
  const handleLoad = (e: SyntheticEvent<HTMLImageElement>) => {
    const img = e.currentTarget
    if (img.naturalWidth <= 1 && img.naturalHeight <= 1) setFailed(true)
  }

  if (!isRenderableCover(src) || failed) {
    return (
      <div className="flex h-full w-full items-center justify-center">
        <BookOpen size={iconSize} className="text-[var(--app-muted)]" />
      </div>
    )
  }

  return (
    <img
      src={src}
      alt={alt}
      loading="lazy"
      referrerPolicy="no-referrer"
      className={className}
      onLoad={handleLoad}
      onError={() => setFailed(true)}
    />
  )
}
