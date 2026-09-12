import { api } from './client'
import type { Media, Playlist } from '../types'

// History rows arrive joined with their Media row; the backend returns null
// for orphaned rows whose media has been removed.
export interface HistoryItem {
  id: string
  user_id: string
  media_id: string
  position_ms: number
  duration_ms: number
  watched_at: string
  completed: boolean
  media?: Media | null
  created_at: string
  updated_at: string
}

export interface PlaylistDetail {
  playlist: Playlist
  items: Media[]
}

export interface ExternalPlayer {
  name: string
  scheme: string
  url: string
}

export interface PlaybackProgressRequest {
  media_id: string
  position_ms: number
  duration_ms: number
  session_id?: string
  session_started_at_ms?: number
  sequence?: number
}

function publicOriginHeader() {
  if (typeof window === 'undefined' || !window.location?.origin) return undefined
  return { 'X-MeBox-Public-Origin': window.location.origin }
}

export const playbackAPI = {
  getResume: (mediaId: string) =>
    api
      .get<{ position_ms: number; duration_ms: number; completed: boolean }>(`/playback/${mediaId}/resume`)
      .then((r) => r.data),

  recordProgress: (payload: PlaybackProgressRequest) =>
    api.post('/history', payload).then((r) => r.data),

  recentHistory: () =>
    api.get<{ items: HistoryItem[] }>('/history').then((r) => r.data.items),

  toggleFavourite: (mediaId: string) =>
    api
      .post<{ favourite: boolean }>(`/favourites/${mediaId}`)
      .then((r) => r.data.favourite),

  listFavourites: () =>
    api.get<{ items: Media[] }>('/favourites').then((r) => r.data.items),

  listPlaylists: () =>
    api.get<{ items: Playlist[] }>('/playlists').then((r) => r.data.items),

  createPlaylist: (name: string, isPublic = false) =>
    api
      .post<Playlist>('/playlists', { name, is_public: isPublic })
      .then((r) => r.data),

  getPlaylist: (id: string) =>
    api.get<PlaylistDetail>(`/playlists/${id}`).then((r) => r.data),

  addToPlaylist: (id: string, mediaId: string) =>
    api.post(`/playlists/${id}/items`, { media_id: mediaId }).then((r) => r.data),

  removeFromPlaylist: (id: string, mediaId: string) =>
    api.delete(`/playlists/${id}/items/${mediaId}`).then((r) => r.data),

  deletePlaylist: (id: string) =>
    api.delete(`/playlists/${id}`).then((r) => r.data),

  externalPlayers: (mediaId: string) =>
    api
      .get<{ players: ExternalPlayer[]; url?: string }>(`/playback/${mediaId}/external-players`, {
        headers: publicOriginHeader(),
      })
      .then((r) => r.data),

  externalURL: (mediaId: string) =>
    api
      .get<{ url: string }>(`/playback/${mediaId}/external-url`, {
        headers: publicOriginHeader(),
      })
      .then((r) => r.data),
}
