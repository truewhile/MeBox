import { useCallback, useMemo, useState, Fragment, type ReactNode } from 'react'
import { useLocation, useParams, useSearchParams } from 'react-router-dom'
import { motion } from 'framer-motion'

import type { Media } from '../types'
import { useAuthStore } from '../stores/auth'
import { isTheatricalFeature, type SeriesCard } from '../utils/groupSeries'
import {
  sortMediaList,
  sortSeriesList,
  type SortField,
  type SortOrder,
} from '../utils/mediaSort'
import { LibraryPageDialogs } from './LibraryPageDialogs'
import { PageBackButton } from '../components/PageBackButton'
import { MediaFavouriteButton } from '../components/MediaFavouriteButton'
import { LibraryPageHeader } from './LibraryPageHeader'
import { LibraryMediaSections } from './LibraryMediaSections'
import { LibrarySeriesDetailSection } from './LibrarySeriesDetailSection'
import { useLibraryData } from './useLibraryData'
import { useLibraryScanStatus } from './useLibraryScanStatus'
import { useLibrarySeriesSelection } from './useLibrarySeriesSelection'
import { useLibraryAdminActions } from './useLibraryAdminActions'
import { usePermission } from '../hooks/usePermission'
import { useFavourites } from '../hooks/useFavourites'

export function LibraryPage() {
  const { id = '' } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  const role = useAuthStore((s) => s.user?.role)
  const canFavorite = usePermission('can_favorite')
  const { isFavourite, toggleFavourite } = useFavourites()
  const [favouriteBusyID, setFavouriteBusyID] = useState('')

  const [scrapeDialogOpen, setScrapeDialogOpen] = useState(false)
  const [manualSeriesScrapeOpen, setManualSeriesScrapeOpen] = useState(false)
  const [seriesMetadataEditOpen, setSeriesMetadataEditOpen] = useState(false)
  const [manualMovie, setManualMovie] = useState<Media | null>(null)

  // 排序状态（支持按库记忆）
  const [sortField, setSortField] = useState<SortField>(() => {
    const saved = localStorage.getItem(`mebox_lib_sort_field_${id}`) || localStorage.getItem('mebox_lib_sort_field')
    return (saved as SortField) || 'title'
  })
  const [sortOrder, setSortOrder] = useState<SortOrder>(() => {
    const saved = localStorage.getItem(`mebox_lib_sort_order_${id}`) || localStorage.getItem('mebox_lib_sort_order')
    return (saved as SortOrder) || 'asc'
  })
  const [randomSeed, setRandomSeed] = useState(() => Date.now())
  const [lastNonRandomSort, setLastNonRandomSort] = useState<SortField>(sortField === 'random' ? 'release_date' : sortField)
  const [lastNonRandomOrder, setLastNonRandomOrder] = useState<SortOrder>(sortOrder)
  const serverSortField = sortField === 'random' ? lastNonRandomSort : sortField
  const serverSortOrder = sortField === 'random' ? lastNonRandomOrder : sortOrder

  // 剧集模式：选中某个剧集后展开详情
  const [selectedSeries, setSelectedSeries] = useState<SeriesCard | null>(null)
  const [selectedSeason, setSelectedSeason] = useState<number | null>(null)

  const {
    library,
    items,
    seriesEpisodeItems,
    total,
    loading,
    loadingMore,
    hasMore,
    loadMore,
    loadAll,
    loadingSeriesEpisodes,
    isSeriesLibrary,
    isSeries,
    seriesCards,
    loadingAllText,
    reloadCurrentLibrary,
  } = useLibraryData(id, selectedSeries, serverSortField, serverSortOrder)

  // 常规排序由服务端全局完成；只有 random 模式才在客户端洗牌。
  const displayedItems = useMemo(() => {
    return sortField === 'random' ? sortMediaList(items, 'random', sortOrder, randomSeed) : items
  }, [items, sortField, sortOrder, randomSeed])

  const displayedSeriesCards = useMemo(() => {
    return sortField === 'random' ? sortSeriesList(seriesCards, 'random', sortOrder, randomSeed) : seriesCards
  }, [seriesCards, sortField, sortOrder, randomSeed])

  const handleSortChange = useCallback(async (field: SortField, order: SortOrder) => {
    if (field === 'random') {
      if (sortField !== 'random') {
        setLastNonRandomSort(sortField)
        setLastNonRandomOrder(sortOrder)
        await loadAll()
      }
      setRandomSeed(Date.now())
    } else {
      setLastNonRandomSort(field)
      setLastNonRandomOrder(order)
    }
    setSortField(field)
    setSortOrder(order)
    if (id) {
      localStorage.setItem(`mebox_lib_sort_field_${id}`, field)
      localStorage.setItem(`mebox_lib_sort_order_${id}`, order)
    }
    localStorage.setItem('mebox_lib_sort_field', field)
    localStorage.setItem('mebox_lib_sort_order', order)
  }, [id, loadAll, sortField, sortOrder])

  const handleLoadMore = useCallback(() => {
    void loadMore()
  }, [loadMore])

  const handleReshuffle = () => {
    setRandomSeed(Date.now())
  }
  const {
    scanning,
    scanProgress,
    handleScan,
  } = useLibraryScanStatus({
    libraryID: id,
    isAdmin: role === 'admin',
    onLibraryChanged: reloadCurrentLibrary,
  })

  const {
    resolvingSeries,
    selectedEpisodes,
    visibleEpisodes,
    selectedSeriesEpisodes,
    selectedSeriesAllEpisodes,
    selectedSeriesMediaIDs,
    handleSeriesClick,
    clearSelectedSeries,
  } = useLibrarySeriesSelection({
    libraryID: id,
    items,
    seriesEpisodeItems,
    isSeriesLibrary,
    isSeries,
    loading,
    seriesCards: displayedSeriesCards,
    searchParams,
    setSearchParams,
    selectedSeries,
    setSelectedSeries,
    selectedSeason,
    setSelectedSeason,
    onClearSeriesState: () => setSeriesMetadataEditOpen(false),
  })
  const selectedSeriesScrapeMedia = selectedSeriesEpisodes.find((media) => !isTheatricalFeature(media))
    ?? selectedSeries?.rep
    ?? null

  const {
    scraping,
    scrapeEpisodeArtwork,
    repairing,
    seriesToolBusy,
    setScrapeEpisodeArtwork,
    handleRepairRescrape,
    handleSeriesSmartScrape,
    handleSeriesProbe,
    handleSeriesNFO,
    handleSeriesOrganize,
    handleSeriesDelete,
    movieActions,
  } = useLibraryAdminActions({
    libraryID: id,
    role,
    library,
    selectedSeries,
    selectedSeriesEpisodes: selectedSeriesAllEpisodes,
    reloadCurrentLibrary,
    clearSelectedSeries,
    setManualMovie,
  })

  const handleToggleFavourite = useCallback(async (mediaID: string) => {
    if (!canFavorite || favouriteBusyID) return
    setFavouriteBusyID(mediaID)
    try {
      await toggleFavourite(mediaID)
    } finally {
      setFavouriteBusyID('')
    }
  }, [canFavorite, favouriteBusyID, toggleFavourite])

  // useCallback 稳定引用：配合 MediaCard 的 memo，仅在收藏状态/操作集变化时
  // 才让卡片重渲染。
  const cardActions = useCallback((media: Media): ReactNode => {
    const actions: ReactNode[] = []
    if (canFavorite) {
      actions.push(
        <MediaFavouriteButton
          key="favourite"
          variant="compact"
          favourite={isFavourite(media.id)}
          disabled={favouriteBusyID === media.id}
          onToggle={() => {
            void handleToggleFavourite(media.id)
          }}
        />,
      )
    }
    const adminActions = movieActions(media)
    if (adminActions) {
      actions.push(<Fragment key="admin-actions">{adminActions}</Fragment>)
    }
    if (actions.length === 0) return undefined
    return <>{actions}</>
  }, [canFavorite, favouriteBusyID, handleToggleFavourite, isFavourite, movieActions])

  // resolvingSeries：深链目标剧集不在已加载的一页卡片里，正在按 key
  // 单独解析。此时先显示加载态，避免先渲染整个媒体库列表再跳走。
  if (loading || resolvingSeries) {
    return (
      <div className="flex items-center justify-center py-32">
        <motion.div animate={{ opacity: [0.4, 1, 0.4] }} transition={{ repeat: Infinity, duration: 2 }} className="flex items-center gap-3">
          <div className="h-2 w-2 rounded-full bg-brand-500" />
          <span className="text-sm text-sand-500">加载中…</span>
        </motion.div>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {!selectedSeries && (
        <div className="hidden lg:block">
          <PageBackButton to="/libraries" label="全部媒体库" compact />
        </div>
      )}

      {!selectedSeries && (
        <LibraryPageHeader
          library={library}
          itemCount={isSeries ? displayedSeriesCards.length : total}
          loadingAllText={loadingAllText}
          scanProgress={scanProgress}
          isAdmin={role === 'admin'}
          scrapeEpisodeArtwork={scrapeEpisodeArtwork}
          scanning={scanning}
          scraping={scraping}
          repairing={repairing}
          sortField={sortField}
          sortOrder={sortOrder}
          onSortChange={handleSortChange}
          onReshuffle={handleReshuffle}
          onScrapeEpisodeArtworkChange={setScrapeEpisodeArtwork}
          onScan={handleScan}
          onScrape={() => setScrapeDialogOpen(true)}
          onRepairRescrape={handleRepairRescrape}
        />
      )}

      <LibraryMediaSections
        isSeries={isSeries}
        items={displayedItems}
        seriesCards={displayedSeriesCards}
        selectedSeries={selectedSeries}
        loading={loading}
        hasMore={hasMore}
        loadingMore={loadingMore}
        onLoadMore={handleLoadMore}
        cardActions={cardActions}
        onSeriesClick={handleSeriesClick}
      />

      <LibrarySeriesDetailSection
        selectedSeries={selectedSeries}
        selectedEpisodes={selectedEpisodes}
        selectedSeason={selectedSeason}
        visibleEpisodes={visibleEpisodes}
        allEpisodes={selectedSeriesEpisodes}
        loadingEpisodes={loadingSeriesEpisodes}
        playbackFrom={`${location.pathname}${location.search}`}
        isAdmin={role === 'admin'}
        canFavorite={canFavorite}
        favourite={selectedSeries ? isFavourite(selectedSeries.rep.id) : false}
        favouriteBusy={!!selectedSeries && favouriteBusyID === selectedSeries.rep.id}
        onToggleFavourite={() => {
          if (!selectedSeries) return
          void handleToggleFavourite(selectedSeries.rep.id)
        }}
        seriesToolBusy={seriesToolBusy}
        onBack={clearSelectedSeries}
        onSmartScrape={handleSeriesSmartScrape}
        onManualScrape={() => setManualSeriesScrapeOpen(true)}
        onMetadataEdit={() => setSeriesMetadataEditOpen(true)}
        onProbe={handleSeriesProbe}
        onNFO={handleSeriesNFO}
        onOrganize={handleSeriesOrganize}
        onDelete={handleSeriesDelete}
        onSeasonChange={setSelectedSeason}
        onManualScrapeMedia={setManualMovie}
      />

      <LibraryPageDialogs
        scrapeDialogOpen={scrapeDialogOpen}
        library={library}
        manualSeriesScrapeOpen={manualSeriesScrapeOpen}
        seriesMetadataEditOpen={seriesMetadataEditOpen}
        manualMovie={manualMovie}
        selectedSeries={selectedSeries}
        selectedSeriesScrapeMedia={selectedSeriesScrapeMedia}
        selectedSeriesMediaIDs={selectedSeriesMediaIDs}
        libraryType={library?.type}
        scrapeEpisodeArtwork={scrapeEpisodeArtwork}
        onScrapeEpisodeArtworkChange={setScrapeEpisodeArtwork}
        onCloseScrapeDialog={() => setScrapeDialogOpen(false)}
        onCloseManualSeriesScrape={() => setManualSeriesScrapeOpen(false)}
        onCloseSeriesMetadataEdit={() => setSeriesMetadataEditOpen(false)}
        onCloseManualMovie={() => setManualMovie(null)}
        onApplied={reloadCurrentLibrary}
      />
    </div>
  )
}
