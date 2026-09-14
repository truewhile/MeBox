export type PlaybackQualitySource = 'cloud' | 'original' | 'local'

export interface PlaybackQuality {
  id: string
  label: string
  height?: number
  source: PlaybackQualitySource
  available: boolean
  requires_transcode?: boolean
  requires_vip?: boolean
  note?: string
}

export interface PlaybackTranscodeState {
  state: 'idle' | 'ready' | 'transcoding' | 'unavailable'
  definition?: string
  message?: string
  retry_after_sec?: number
  started_at?: number
}

export interface PlaybackInfo {
  media_id: string
  provider: string
  fallback: string[]
  default_quality: string
  cloud_qualities?: PlaybackQuality[]
  local_qualities: PlaybackQuality[]
  transcode: PlaybackTranscodeState
}

