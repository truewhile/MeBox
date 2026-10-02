import { useEffect, useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'

import { useSmoothWheelScroll } from '../../hooks/useSmoothWheelScroll'
import { rememberImageSize } from '../../utils/comicSpread'
import { comicImageSizing, isSelfSizedComicFit } from '../../utils/comicImageFit'
import type { ComicImageFit } from '../../stores/readerSettings'

// 漫画/图片阅读器（仿 legado MangaMenu 能力面）：
// 上下滚动（默认）/ 左右单页 / 左右双页三种呈现；点击分区翻页/呼出菜单；图片懒加载。
//
// 双页铺开由外层算好「这一屏显示哪几张」传进来（见 utils/comicSpread.ts），
// 这里只负责把它们并排摆好、各自撑满视口高度。
// 滚动模式的显示尺寸由 imageFit 档位决定（见 utils/comicImageFit.ts）。

interface ReaderComicProps {
  images: string[]
  theme: { bg: string; text: string; accent: string }
  mode: 'page' | 'scroll'
  page: number // 单页模式当前页（0 基）
  onZone: (zone: 'left' | 'center' | 'right') => void
  initialImage: number
  onProgress: (imageIndex: number) => void
  scrollTo: number | null // 滚动模式：外部要求滚动到的图片序号
  onScrolled: () => void
  /** 双页模式：本屏要并排显示的图片序号（1 张=独占的宽图/单页，2 张=左右一对）。 */
  spread?: number[] | null
  /** 滚动模式的图片显示尺寸档位（翻页模式不适用，传 default 即可）。 */
  imageFit?: ComicImageFit
}

function ComicImage({
  src,
  theme,
  fit = false,
  half = false,
  imageFit = 'default',
  viewportHeight = 0,
}: {
  src: string
  theme: { bg: string; text: string; accent: string }
  // fit：整页缩放至视口内，长图不再被 overflow-hidden 的容器裁掉（翻页模式）。
  fit?: boolean
  // half：双页并排时占位不超过半屏；容器收窄到图片本身宽度，两页之间不留缝。
  half?: boolean
  // imageFit：滚动模式的显示尺寸档位。
  imageFit?: ComicImageFit
  // viewportHeight：滚动容器高度（px），height/long 档位按它换算。
  viewportHeight?: number
}) {
  const [state, setState] = useState<'loading' | 'ok' | 'error'>('loading')

  // 换章后 src 变了要重新回到 loading：否则上一章加载失败的第 N 张会一直
  // 显示「图片加载失败」，而 key 不变（滚动模式按序号做 key）不会重挂载。
  useEffect(() => {
    setState('loading')
  }, [src])

  const loaded = state === 'ok'
  // 翻页模式：整页缩放进视口。滚动模式：按用户选的尺寸档位摆（外层列宽已放开）。
  const sizing = fit
    ? {
        className: 'block max-h-full w-auto max-w-full object-contain',
        style: loaded ? {} : { minHeight: '10rem' },
      }
    : comicImageSizing(imageFit, viewportHeight, loaded)

  // <img> 必须始终留在渲染树里（不能 display:none）：浏览器不会去拉取
  // display:none 的 loading="lazy" 图片，onLoad 就永远不会触发，于是更没机会
  // 脱离 loading —— 之前用 hidden 藏图就死在这里，漫画只剩一个转圈。
  // 改成图片正常参与布局，未加载时用 minHeight 占位，转圈/失败信息盖在上层。
  return (
    <div
      className={`relative flex items-center justify-center ${fit ? 'h-full' : ''} ${half ? '' : 'w-full'}`}
      style={{
        backgroundColor: theme.bg,
        // 半屏占位：宽度取「图片自然宽度」与「半屏」的较小值。容器是 flex item，
        // 百分比 max-width 相对这一行的宽度解析，所以图片不会撑破半屏；
        // 图片比半屏窄时容器跟着收窄，左右两页自然贴在一起。
        ...(half ? { maxWidth: '50%', minWidth: 0 } : null),
      }}
    >
      <img
        src={src}
        loading="lazy"
        alt=""
        onLoad={(e) => {
          const el = e.currentTarget
          // 顺带回填原始尺寸：双页分组要用它判断这张图是否横跨两页
          rememberImageSize(src, el.naturalWidth, el.naturalHeight)
          setState('ok')
        }}
        onError={() => setState('error')}
        className={sizing.className}
        style={sizing.style}
      />
      {state === 'loading' && (
        <div
          className="pointer-events-none absolute inset-0 flex items-center justify-center"
          style={{ color: theme.text }}
        >
          <Loader2 className="animate-spin opacity-50" size={22} />
        </div>
      )}
      {state === 'error' && (
        <div
          className="pointer-events-none absolute inset-0 flex items-center justify-center text-xs opacity-60"
          style={{ color: theme.text }}
        >
          图片加载失败
        </div>
      )}
    </div>
  )
}

export function ReaderComic({ images, theme, mode, page, onZone, initialImage, onProgress, scrollTo, onScrolled, spread, imageFit = 'default' }: ReaderComicProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const imgRefs = useRef<(HTMLDivElement | null)[]>([])
  const restoredRef = useRef(false)
  // 滚动容器的高度：「一屏多高」以这个容器为准，而不是 window.innerHeight——正文区是
  // root（fixed inset-0）里的 flex-1，将来上下再挂条之类的布局变化就不等于窗口高度了。
  // 用 ResizeObserver 跟着它，改窗口大小时 height/long 档位自动重算。
  const [viewportHeight, setViewportHeight] = useState(0)

  const selfSized = isSelfSizedComicFit(imageFit)
  useEffect(() => {
    // 只有 height/long/original 用得到，默认档位不挂观察器，免得每次改窗口都重渲染整章。
    if (mode !== 'scroll' || !selfSized) return
    const el = scrollRef.current
    if (!el) return
    const update = () => setViewportHeight(el.clientHeight)
    update()
    const observer = new ResizeObserver(update)
    observer.observe(el)
    return () => observer.disconnect()
  }, [mode, selfSized])

  // 滚动模式：恢复进度（图片序号）并上报当前图
  useEffect(() => {
    if (mode !== 'scroll') return
    if (!restoredRef.current && initialImage > 0 && imgRefs.current[initialImage]) {
      imgRefs.current[initialImage]?.scrollIntoView({ block: 'start' })
    }
    restoredRef.current = true
  }, [mode, initialImage, images.length])

  // 外部要求滚动到指定图片（菜单进度条）
  useEffect(() => {
    if (mode !== 'scroll' || scrollTo === null) return
    imgRefs.current[scrollTo]?.scrollIntoView({ block: 'start' })
    onScrolled()
  }, [scrollTo, mode, onScrolled])

  useEffect(() => {
    if (mode !== 'page') return
    restoredRef.current = false
  }, [mode])

  // 上下滚动模式的滚轮同样走「类手机滑动」的平滑惯性滚动
  useSmoothWheelScroll(scrollRef, mode === 'scroll')

  if (images.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-sm opacity-60" style={{ color: theme.text }}>
        本章没有图片
      </div>
    )
  }

  if (mode === 'page') {
    // 翻页模式：翻页由外层点击区驱动（page 语义 = 图片序号）。
    // 双页铺开时外层按「屏」给好成员（spread），这里只负责并排摆开。
    const group = (spread ?? []).filter((i) => i >= 0 && i < images.length)
    const idxs = group.length > 0 ? group : [Math.min(Math.max(page, 0), images.length - 1)]
    const paired = idxs.length > 1
    return (
      <div className="flex h-full items-center justify-center">
        {idxs.map((i) => (
          <ComicImage key={images[i]} src={images[i]} theme={theme} fit half={paired} />
        ))}
      </div>
    )
  }

  // 上下滚动模式
  return (
    <div
      ref={scrollRef}
      className="h-full overflow-y-auto"
      onScroll={(e) => {
        const el = e.currentTarget
        // 以视口顶部所在图片为当前进度
        let current = 0
        for (let i = 0; i < imgRefs.current.length; i++) {
          const node = imgRefs.current[i]
          if (node && node.offsetTop <= el.scrollTop + el.clientHeight * 0.4) current = i
        }
        onProgress(current)
        if (!restoredRef.current && initialImage >= 0) {
          restoredRef.current = true
        }
      }}
    >
      {images.map((src, i) => (
        <div
          key={i}
          ref={(node) => {
            imgRefs.current[i] = node
          }}
          onClick={(e) => {
            const rect = e.currentTarget.getBoundingClientRect()
            const x = (e.clientX - rect.left) / rect.width
            if (x < 0.3) {
              scrollRef.current?.scrollBy({ top: -window.innerHeight * 0.9, behavior: 'auto' })
            } else if (x > 0.7) {
              scrollRef.current?.scrollBy({ top: window.innerHeight * 0.9, behavior: 'auto' })
            } else {
              onZone('center')
            }
          }}
        >
          <ComicImage src={src} theme={theme} imageFit={imageFit} viewportHeight={viewportHeight} />
          <p className="pb-1 text-center text-2xs opacity-40" style={{ color: theme.text }}>
            {i + 1} / {images.length}
          </p>
        </div>
      ))}
      <div className="h-10" />
    </div>
  )
}
