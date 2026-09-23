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

/** 可跳过的区间类型，与后端 media_segments.kind 一一对应。 */
export type PlaybackSegmentKind = 'intro' | 'recap' | 'credits' | 'preview'

export interface PlaybackSegment {
  kind: PlaybackSegmentKind
  start_ms: number
  /** 0 表示区间一直延续到片尾（后端把 end_ms: null 落成 0），需按媒体时长补齐。 */
  end_ms: number
}

export interface PlaybackSegmentsResponse {
  segments: PlaybackSegment[]
  /** 当前生效播放档案的「自动跳过片头」开关。 */
  auto_skip: boolean
  /**
   * true 表示 ffprobe 章节提取还在服务端后台跑：这次可能还没有章节数据，隔几秒
   * 再拉一次就能拿到。拿到时若片头还没播完，跳过按钮会自动出现；已经过了片头
   * 时间则不会提示。
   */
  pending?: boolean
  /** 本次实际生效的数据来源。 */
  source?: PlaybackSegmentSource
}

/** 片头/片尾数据来源，与后端 play_profiles.segment_source 一致。 */
export type PlaybackSegmentSource = 'auto' | 'theintrodb' | 'ffprobe'

