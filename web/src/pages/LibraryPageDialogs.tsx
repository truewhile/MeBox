import { ManualScrapeDialog } from '../components/ManualScrapeDialog'
import { MetadataEditDialog } from '../components/MetadataEditDialog'
import { ScrapeMetadataDialog } from '../components/ScrapeMetadataDialog'
import type { Library, Media } from '../types'
import { isTheatricalFeature, seriesTitle, type SeriesCard } from '../utils/groupSeries'

type LibraryPageDialogsProps = {
  scrapeDialogOpen: boolean
  library: Library | null
  manualSeriesScrapeOpen: boolean
  seriesMetadataEditOpen: boolean
  manualMovie: Media | null
  selectedSeries: SeriesCard | null
  selectedSeriesScrapeMedia: Media | null
  selectedSeriesMediaIDs: string[]
  libraryType?: string
  scrapeEpisodeArtwork: boolean
  onScrapeEpisodeArtworkChange: (checked: boolean) => void
  onCloseScrapeDialog: () => void
  onCloseManualSeriesScrape: () => void
  onCloseSeriesMetadataEdit: () => void
  onCloseManualMovie: () => void
  onApplied: () => void
}

export function LibraryPageDialogs({
  scrapeDialogOpen,
  library,
  manualSeriesScrapeOpen,
  seriesMetadataEditOpen,
  manualMovie,
  selectedSeries,
  selectedSeriesScrapeMedia,
  selectedSeriesMediaIDs,
  libraryType,
  scrapeEpisodeArtwork,
  onScrapeEpisodeArtworkChange,
  onCloseScrapeDialog,
  onCloseManualSeriesScrape,
  onCloseSeriesMetadataEdit,
  onCloseManualMovie,
  onApplied,
}: LibraryPageDialogsProps) {
  const selectedSeriesTitle = selectedSeries ? seriesTitle(selectedSeries.rep) : ''

  return (
    <>
      <ScrapeMetadataDialog
        open={scrapeDialogOpen}
        library={library}
        scrapeEpisodeArtwork={scrapeEpisodeArtwork}
        onScrapeEpisodeArtworkChange={onScrapeEpisodeArtworkChange}
        onClose={onCloseScrapeDialog}
        onCompleted={onApplied}
      />
      <ManualScrapeDialog
        open={manualSeriesScrapeOpen}
        media={selectedSeriesScrapeMedia}
        mediaIds={selectedSeriesMediaIDs}
        defaultQuery={selectedSeriesTitle}
        mediaType="tv"
        scopeLabel={selectedSeriesTitle || '当前剧集'}
        episodeArtwork={scrapeEpisodeArtwork}
        onClose={onCloseManualSeriesScrape}
        onApplied={onApplied}
      />
      <MetadataEditDialog
        open={seriesMetadataEditOpen}
        media={selectedSeries?.rep ?? null}
        mediaIds={selectedSeriesMediaIDs}
        mode="series"
        scopeLabel={selectedSeriesTitle || '当前剧集'}
        onClose={onCloseSeriesMetadataEdit}
        onSaved={onApplied}
      />
      <ManualScrapeDialog
        open={!!manualMovie}
        media={manualMovie}
        defaultQuery={manualMovie?.title ?? ''}
        mediaType={manualMovie ? scrapeMediaType(libraryType, manualMovie) : libraryType || 'movie'}
        scopeLabel={manualMovie?.title ?? '当前电影'}
        episodeArtwork={scrapeEpisodeArtwork}
        onClose={onCloseManualMovie}
        onApplied={onApplied}
      />
    </>
  )
}

function scrapeMediaType(libraryType: string | undefined, media: Media): string {
  if (isTheatricalFeature(media)) {
    return 'movie'
  }
  if ((media.season_num ?? 0) > 0 || (media.episode_num ?? 0) > 0) {
    return 'tv'
  }
  return libraryType || 'movie'
}
