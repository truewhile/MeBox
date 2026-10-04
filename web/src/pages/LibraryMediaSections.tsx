import { memo, type ReactNode } from 'react'
import { Film } from 'lucide-react'

import { LoadMoreSentinel } from '../components/LoadMoreSentinel'
import { MediaCard } from '../components/MediaCard'
import { MEDIA_GRID_CLASS, VirtualMediaGrid } from '../components/VirtualMediaGrid'
import type { Media } from '../types'
import type { SeriesCard } from '../utils/groupSeries'

// 超过该数量后切换为虚拟滚动：DOM 只挂载视口内的卡片，大库不再线性劣化。
const VIRTUALIZE_THRESHOLD = 200

type LibraryMediaSectionsProps = {
  isSeries: boolean
  items: Media[]
  seriesCards: SeriesCard[]
  selectedSeries: SeriesCard | null
  loading: boolean
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
  cardActions: (media: Media) => ReactNode
  onSeriesClick: (series: SeriesCard) => void
}

export function LibraryMediaSections({
  isSeries,
  items,
  seriesCards,
  selectedSeries,
  loading,
  hasMore,
  loadingMore,
  onLoadMore,
  cardActions,
  onSeriesClick,
}: LibraryMediaSectionsProps) {
  return (
    <>
      {!isSeries && items.length > 0 && (
        <MediaGrid count={items.length} renderItem={(index) => {
          const media = items[index]
          // renderActions 传函数引用：MediaCard 重渲染时才构建操作按钮，
          // 配合 memo，父级无关状态变化不再级联重渲染全部卡片。
          return <MediaCard key={media.id} media={media} renderActions={cardActions} />
        }} />
      )}

      {!isSeries && items.length === 0 && (
        <LibraryEmptyState message="该媒体库暂无内容，触发一次扫描后再来看看" />
      )}

      {isSeries && seriesCards.length > 0 && !selectedSeries && (
        <MediaGrid count={seriesCards.length} renderItem={(index) => {
          const series = seriesCards[index]
          return (
            <SeriesCardItem
              key={series.key}
              series={series}
              cardActions={cardActions}
              onSeriesClick={onSeriesClick}
            />
          )
        }} />
      )}

      {isSeries && seriesCards.length === 0 && !loading && (
        <LibraryEmptyState message="该库尚未发现任何剧集，触发一次扫描后再来看看" />
      )}

      <LoadMoreSentinel hasMore={hasMore} loadingMore={loadingMore} onLoadMore={onLoadMore} />
    </>
  )
}

// 独立 memo 组件：onClick 闭包在其内部创建，props 均为稳定引用，
// 父级重渲染不会穿透到每张剧集卡片。
const SeriesCardItem = memo(function SeriesCardItem({
  series,
  cardActions,
  onSeriesClick,
}: {
  series: SeriesCard
  cardActions: (media: Media) => ReactNode
  onSeriesClick: (series: SeriesCard) => void
}) {
  return (
    <MediaCard
      media={series.rep}
      count={series.count}
      renderActions={cardActions}
      onClick={() => onSeriesClick(series)}
    />
  )
})

function MediaGrid({ count, renderItem }: { count: number; renderItem: (index: number) => ReactNode }) {
  if (count <= VIRTUALIZE_THRESHOLD) {
    return <div className={MEDIA_GRID_CLASS}>{Array.from({ length: count }, (_, index) => renderItem(index))}</div>
  }
  return <VirtualMediaGrid totalCount={count} renderItem={renderItem} />
}

function LibraryEmptyState({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-24 text-center">
      <Film className="mb-4 h-12 w-12 text-gray-500" />
      <p className="text-ink-50">{message}</p>
    </div>
  )
}