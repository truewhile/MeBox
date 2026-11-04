import { AnimatePresence, motion } from 'framer-motion'

import type { Media } from '../types'
import type { SeriesCard } from '../utils/groupSeries'
import type { EpisodeProgress, MediaDetailResume } from './mediaDetailResumeModel'
import { LibrarySeriesDetailHeader } from './LibrarySeriesDetailHeader'
import { LibrarySeriesEpisodes } from './LibrarySeriesEpisodes'

type SeasonEpisodes = {
  season: number
  episodes: Media[]
}

type LibrarySeriesDetailSectionProps = {
  selectedSeries: SeriesCard | null
  selectedEpisodes: SeasonEpisodes[]
  selectedSeason: number | null
  visibleEpisodes: Media[]
  allEpisodes: Media[]
  resume: MediaDetailResume | null
  episodeProgress: Map<string, EpisodeProgress>
  loadingEpisodes: boolean
  playbackFrom: string
  isAdmin: boolean
  canFavorite: boolean
  favourite: boolean
  favouriteBusy: boolean
  onToggleFavourite: () => void
  seriesToolBusy: string
  onBack: () => void
  onSmartScrape: () => void
  onManualScrape: () => void
  onMetadataEdit: () => void
  onProbe: () => void
  onNFO: () => void
  onOrganize: () => void
  onDelete: () => void
  onSeasonChange: (season: number) => void
  onManualScrapeMedia: (media: Media) => void
}

export function LibrarySeriesDetailSection({
  selectedSeries,
  selectedEpisodes,
  selectedSeason,
  visibleEpisodes,
  allEpisodes,
  resume,
  episodeProgress,
  loadingEpisodes,
  playbackFrom,
  isAdmin,
  canFavorite,
  favourite,
  favouriteBusy,
  onToggleFavourite,
  seriesToolBusy,
  onBack,
  onSmartScrape,
  onManualScrape,
  onMetadataEdit,
  onProbe,
  onNFO,
  onOrganize,
  onDelete,
  onSeasonChange,
  onManualScrapeMedia,
}: LibrarySeriesDetailSectionProps) {
  return (
    <AnimatePresence mode="wait">
      {selectedSeries && (
        <motion.div
          initial={{ opacity: 0, y: 16 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0 }}
          className="space-y-6"
        >
          <LibrarySeriesDetailHeader
            series={selectedSeries}
            allEpisodes={allEpisodes}
            resume={resume}
            playbackFrom={playbackFrom}
            isAdmin={isAdmin}
            canFavorite={canFavorite}
            favourite={favourite}
            favouriteBusy={favouriteBusy}
            onToggleFavourite={onToggleFavourite}
            seriesToolBusy={seriesToolBusy}
            onBack={onBack}
            onSmartScrape={onSmartScrape}
            onManualScrape={onManualScrape}
            onMetadataEdit={onMetadataEdit}
            onProbe={onProbe}
            onNFO={onNFO}
            onOrganize={onOrganize}
            onDelete={onDelete}
          />

          <LibrarySeriesEpisodesPanel
            loading={loadingEpisodes}
            selectedEpisodes={selectedEpisodes}
            selectedSeason={selectedSeason}
            visibleEpisodes={visibleEpisodes}
            progressById={episodeProgress}
            playbackFrom={playbackFrom}
            isAdmin={isAdmin}
            onSeasonChange={onSeasonChange}
            onManualScrape={onManualScrapeMedia}
          />
        </motion.div>
      )}
    </AnimatePresence>
  )
}

type LibrarySeriesEpisodesPanelProps = {
  loading: boolean
  selectedEpisodes: SeasonEpisodes[]
  selectedSeason: number | null
  visibleEpisodes: Media[]
  progressById: Map<string, EpisodeProgress>
  playbackFrom: string
  isAdmin: boolean
  onSeasonChange: (season: number) => void
  onManualScrape: (media: Media) => void
}

function LibrarySeriesEpisodesPanel({
  loading,
  selectedEpisodes,
  selectedSeason,
  visibleEpisodes,
  progressById,
  playbackFrom,
  isAdmin,
  onSeasonChange,
  onManualScrape,
}: LibrarySeriesEpisodesPanelProps) {
  return (
    <div className="space-y-6">
      <LibrarySeriesEpisodes
        loading={loading}
        selectedEpisodes={selectedEpisodes}
        selectedSeason={selectedSeason}
        visibleEpisodes={visibleEpisodes}
        progressById={progressById}
        playbackFrom={playbackFrom}
        isAdmin={isAdmin}
        onSeasonChange={onSeasonChange}
        onManualScrape={onManualScrape}
      />
    </div>
  )
}
