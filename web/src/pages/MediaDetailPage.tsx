import { useNavigate, useParams } from 'react-router-dom'

import { useAuthStore } from '../stores/auth'
import { MediaDetailBackdrop } from './MediaDetailArtwork'
import {
  MediaDetailBackButton,
  MediaDetailDialogs,
  MediaDetailLoading,
  MediaDetailMainContent,
  MediaDetailMissing,
} from './MediaDetailPageSections'
import { LibrarySeriesEpisodes } from './LibrarySeriesEpisodes'
import { useMediaDetailPageState } from './useMediaDetailPageState'

export function MediaDetailPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const role = useAuthStore((s) => s.user?.role)
  const detail = useMediaDetailPageState({ id, navigate })

  if (detail.loading) return <MediaDetailLoading />
  if (!detail.media) return <MediaDetailMissing />
  const media = detail.media

  return (
    <div className="relative overflow-hidden rounded-3xl bg-white border border-gray-200/90 shadow-[0_1px_3px_rgba(0,0,0,0.01),0_1px_2px_rgba(0,0,0,0.015)]">
      <MediaDetailBackdrop media={media} />

      <MediaDetailBackButton onBack={detail.goBack} />

      <MediaDetailMainContent
        media={media}
        isAdmin={role === 'admin'}
        favourite={detail.favourite}
        playTargetId={media.id || detail.firstPlayableEpisode?.id}
        scrapeEpisodeArtwork={detail.scrapeEpisodeArtwork}
        onToggleFavourite={detail.toggleFavourite}
        onScrapeEpisodeArtworkChange={detail.setScrapeEpisodeArtwork}
        onSmartScrape={detail.rescrape}
        onManualScrape={() => detail.setManualScrapeOpen(true)}
        onMetadataEdit={() => detail.setMetadataEditOpen(true)}
        onOrganize={() => detail.setOrganizeOpen(true)}
        onProbe={detail.reprobe}
        onExportNFO={detail.exportNFO}
        onDelete={detail.deleteMedia}
      />

      {detail.hasEpisodes && (
        <div className="relative z-10 px-6 pb-8 sm:px-10 space-y-6">
          <div className="divider border-gray-200/60" />
          <LibrarySeriesEpisodes
            loading={detail.loadingEpisodes}
            selectedEpisodes={detail.seasonGroups}
            selectedSeason={detail.selectedSeason}
            visibleEpisodes={detail.visibleEpisodes}
            playbackFrom={`/media/${media.id}`}
            onSeasonChange={detail.setSelectedSeason}
          />
        </div>
      )}

      <MediaDetailDialogs
        media={media}
        manualScrapeOpen={detail.manualScrapeOpen}
        metadataEditOpen={detail.metadataEditOpen}
        organizeOpen={detail.organizeOpen}
        scrapeEpisodeArtwork={detail.scrapeEpisodeArtwork}
        onManualScrapeClose={() => detail.setManualScrapeOpen(false)}
        onMetadataEditClose={() => detail.setMetadataEditOpen(false)}
        onOrganizeClose={() => detail.setOrganizeOpen(false)}
        onManualScrapeApplied={detail.refresh}
        onMetadataSaved={detail.handleMetadataSaved}
        onOrganized={detail.refresh}
      />
    </div>
  )
}
