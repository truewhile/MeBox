import { memo, type ReactNode } from 'react'
import { Film } from 'lucide-react'

import { LoadMoreSentinel } from '../components/LoadMoreSentinel'
import { MediaCard } from '../components/MediaCard'
import { MEDIA_GRID_CLASS, VirtualMediaGrid } from '../components/VirtualMediaGrid'
import type { Media } from '../types'
import type { SeriesCard } from '../utils/groupSeries'

// 超过该数量后切换为虚拟滚动：DOM 只挂载视口内的卡片，大库不再线性劣化。
// 判定必须用“库的总量”而不是“已加载条数”：分页补页会把已加载条数推过阈值，
// 若此时才切换渲染方式，整块网格的 DOM 子树会被替换、容器高度瞬间塌陷，
// 浏览器随之把 scrollTop 夹回顶部，滚动记忆恢复得再好也会当场失效。
const VIRTUALIZE_THRESHOLD = 200

type LibraryMediaSectionsProps = {
  isSeries: boolean
  /** 当前列表的总条数（媒体库总量），用于稳定地决定是否走虚拟滚动。 */
  total: number
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
  total,
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
  // 渲染方式在整次浏览里保持不变：总量超过阈值就一开始就用虚拟列表。
  const virtualizeByTotal = total > VIRTUALIZE_THRESHOLD

  return (
    <>
      {!isSeries && items.length > 0 && (
        <MediaGrid
          count={items.length}
          virtualize={virtualizeByTotal}
          renderItem={(index) => {
            const media = items[index]
            // renderActions 传函数引用：MediaCard 重渲染时才构建操作按钮，
            // 配合 memo，父级无关状态变化不再级联重渲染全部卡片。
            return <MediaCard key={media.id} media={media} renderActions={cardActions} />
          }}
        />
      )}

      {!isSeries && items.length === 0 && (
        <LibraryEmptyState message="该媒体库暂无内容，触发一次扫描后再来看看" />
      )}

      {isSeries && seriesCards.length > 0 && !selectedSeries && (
        <MediaGrid
          count={seriesCards.length}
          virtualize={virtualizeByTotal}
          renderItem={(index) => {
            const series = seriesCards[index]
            return (
              <SeriesCardItem
                key={series.key}
                series={series}
                cardActions={cardActions}
                onSeriesClick={onSeriesClick}
              />
            )
          }}
        />
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

function MediaGrid({
  count,
  virtualize,
  renderItem,
}: {
  count: number
  virtualize: boolean
  renderItem: (index: number) => ReactNode
}) {
  if (!virtualize) {
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