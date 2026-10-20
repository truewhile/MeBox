import { useEffect, useRef } from 'react'

type LoadMoreSentinelProps = {
  /** 还有下一页；false 时整个哨兵不渲染（没有更多了）。 */
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
  /** 上一页加载失败的原因：停掉自动加载、改成手动重试，见下方 observer 注释。 */
  error?: string
  onRetry?: () => void
  label?: string
  loadingLabel?: string
}

/**
 * 触底加载哨兵：自身进入滚动容器（应用主滚动区 #app-main-scroll）附近时回调
 * onLoadMore，同时留一个可点的按钮兜底（观察器不可用、或用户想手动催一下）。
 *
 * 失败时必须停掉观察器：失败后 loadingMore 会回到 false，观察器一旦重建就会
 * 立刻再次命中（哨兵还在视口里），变成「失败即重试」的死循环；所以 error 非空
 * 时只渲染重试按钮，等用户点了、错误清掉再重新挂观察器。
 */
export function LoadMoreSentinel({
  hasMore,
  loadingMore,
  onLoadMore,
  error = '',
  onRetry,
  label = '加载更多',
  loadingLabel = '加载中…',
}: LoadMoreSentinelProps) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const element = ref.current
    if (!element || !hasMore || error) return
    // 观察根是主滚动容器而不是视口：页面是嵌在 #app-main-scroll 里滚的。
    const root = document.getElementById('app-main-scroll')
    const observer = new IntersectionObserver(
      (entries) => {
        if (!loadingMore && entries.some((entry) => entry.isIntersecting)) {
          onLoadMore()
        }
      },
      { root, rootMargin: '600px 0px', threshold: 0 },
    )
    observer.observe(element)
    return () => observer.disconnect()
  }, [hasMore, loadingMore, onLoadMore, error])

  if (!hasMore) return null

  const showRetry = !!error && !!onRetry
  return (
    <div ref={ref} className="flex flex-col items-center gap-1.5 py-8">
      {error && <p className="text-2xs text-[var(--app-muted)]">{error}</p>}
      <button
        type="button"
        disabled={loadingMore}
        onClick={showRetry ? onRetry : onLoadMore}
        className="rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] px-4 py-2 text-sm font-bold text-[var(--app-subtle)] transition-colors hover:bg-[var(--app-hover)] hover:text-[var(--app-text)] disabled:cursor-wait disabled:opacity-60"
      >
        {loadingMore ? loadingLabel : label}
      </button>
    </div>
  )
}
