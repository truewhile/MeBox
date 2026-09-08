import { api } from './client'
import { useAuthStore } from '../stores/auth'

export interface SubtitleTrack {
  lang: string
  label: string
  path: string
  url: string
  codec: string
  source: 'external' | 'embedded'
  delivery: 'webvtt' | 'burn'
  stream_index?: number
}

export const subtitlesAPI = {
  list: (mediaId: string, includeEmbedded = false) =>
    api
      .get<{ tracks: SubtitleTrack[] | null }>(`/media/${mediaId}/subtitles`, {
        params: includeEmbedded ? { include_embedded: 'true' } : undefined,
      })
      .then((r) => r.data.tracks ?? []),

  url: (mediaId: string, path: string) => {
    const token = useAuthStore.getState().token ?? ''
    return `/api/subtitles/${encodeURIComponent(mediaId)}?path=${encodeURIComponent(
      path,
    )}&token=${encodeURIComponent(token)}`
  },
}
