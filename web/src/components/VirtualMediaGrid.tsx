import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Virtuoso } from 'react-virtuoso'
import clsx from 'clsx'

// 与 LibraryMediaSections 等处的海报网格保持同一套响应式列配置。
export const MEDIA_GRID_CLASS =
  'grid grid-cols-3 gap-4 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-7 2xl:grid-cols-8'

function getFallbackColumns(width: number): number {
  if (width >= 1536) return 8
  if (width >= 1280) return 7
  if (width >= 1024) return 6
  if (width >= 768) return 5
  if (width >= 640) return 4
  return 3
}

// VirtualMediaGrid 大库性能优化：
// 采用按行虚拟滚动（Row-based Virtualization）。相比 VirtuosoGrid 强制要求所有网格项
// 绝对等高且易受 CSS Grid 亚像素尺寸扰动引发死循环闪烁，按行使用基础 Virtuoso 组件
// 天然支持每行真实高度，并且每一行内部保持原生的响应式 CSS Grid 布局。
export function VirtualMediaGrid({
  totalCount,
  renderItem,
}: {
  totalCount: number
  renderItem: (index: number) => ReactNode
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const probeRef = useRef<HTMLDivElement>(null)
  const [columns, setColumns] = useState(() => {
    if (typeof window !== 'undefined') {
      return getFallbackColumns(window.innerWidth)
    }
    return 4
  })

  const updateColumns = useCallback(() => {
    if (probeRef.current) {
      const computed = window.getComputedStyle(probeRef.current).gridTemplateColumns
      if (computed && computed !== 'none') {
        const count = computed.trim().split(/\s+/).filter(Boolean).length
        if (count > 0) {
          setColumns((prev) => (prev !== count ? count : prev))
          return
        }
      }
    }
    const width = containerRef.current?.clientWidth || (typeof window !== 'undefined' ? window.innerWidth : 0)
    if (width > 0) {
      const fallback = getFallbackColumns(width)
      setColumns((prev) => (prev !== fallback ? fallback : prev))
    }
  }, [])

  useLayoutEffect(() => {
    updateColumns()
  }, [updateColumns])

  useEffect(() => {
    const el = containerRef.current
    if (!el || typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', updateColumns)
      return () => window.removeEventListener('resize', updateColumns)
    }
    const observer = new ResizeObserver(() => {
      updateColumns()
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [updateColumns])

  const [scrollParent, setScrollParent] = useState<HTMLElement | null>(() => {
    return typeof document !== 'undefined' ? document.getElementById('app-main-scroll') : null
  })

  useEffect(() => {
    if (!scrollParent) {
      setScrollParent(document.getElementById('app-main-scroll'))
    }
  }, [scrollParent])

  const rowCount = Math.ceil(totalCount / columns)

  return (
    <div ref={containerRef} className="relative w-full">
      {/* 隐藏探针节点：跟随 Tailwind MEDIA_GRID_CLASS 响应式断点自动计算当前列数 */}
      <div
        ref={probeRef}
        className={clsx(MEDIA_GRID_CLASS, 'pointer-events-none invisible absolute h-0 w-full overflow-hidden')}
        aria-hidden="true"
      />

      {!scrollParent ? (
        <div className={MEDIA_GRID_CLASS}>
          {Array.from({ length: Math.min(totalCount, columns * 4) }, (_, index) => (
            <div key={index}>{renderItem(index)}</div>
          ))}
        </div>
      ) : (
        <Virtuoso
          customScrollParent={scrollParent}
          totalCount={rowCount}
          overscan={800}
          itemContent={(rowIndex) => {
            const start = rowIndex * columns
            return (
              <div className={clsx(MEDIA_GRID_CLASS, rowIndex < rowCount - 1 && 'pb-4')}>
                {Array.from({ length: columns }, (_, colIndex) => {
                  const itemIndex = start + colIndex
                  if (itemIndex >= totalCount) {
                    return <div key={colIndex} aria-hidden="true" />
                  }
                  return <div key={itemIndex}>{renderItem(itemIndex)}</div>
                })}
              </div>
            )
          }}
        />
      )}
    </div>
  )
}
