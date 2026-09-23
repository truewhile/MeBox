import { api } from './client'
import type { PlayProfile } from '../types'

// Payload accepted by create / update.
export interface PlayProfileInput {
  user_id?: string
  name: string
  is_default: boolean
  content_rating_limit?: string
  allow_adult: boolean
  require_pin: boolean
  pin?: string
  preferred_subtitle_lang?: string
  preferred_audio_lang?: string
  autoplay_next: boolean
  skip_intro: boolean
  allowed_library_ids: string[]
}

export interface PlayProfilePINVerifyResponse {
  profile: PlayProfile
  token: string
  expires_at: string
}

// playProfilesAPI wraps caller-scoped /play-profiles.
export const playProfilesAPI = {
  list: () => api.get<PlayProfile[]>('/play-profiles').then((r) => r.data),

  create: (input: PlayProfileInput) =>
    api.post<PlayProfile>('/play-profiles', input).then((r) => r.data),

  update: (id: string, input: PlayProfileInput) =>
    api.put<PlayProfile>(`/play-profiles/${id}`, input).then((r) => r.data),

  verifyPin: (id: string, pin: string) =>
    api
      .post<PlayProfilePINVerifyResponse>(`/play-profiles/${id}/verify-pin`, { pin })
      .then((r) => r.data),

  remove: (id: string, proof?: { pin?: string; password?: string }) =>
    api.delete(`/play-profiles/${id}`, { data: proof ?? {} }).then((r) => r.data),
}
