import type { Media } from '../types'
import { isRemoteEmbyID } from '../utils/remoteEmby'

export type PlayerMode = 'direct' | 'hls'

const directContainers = ['mp4', 'webm', 'm4v']
const directVideoCodecs = ['h264', 'avc', 'avc1']
const directAudioCodecs = ['aac', 'mp3', 'opus']

/**
 * 远程 Emby 挂载：本地没有原始文件，网页端只能直连，不能转码。
 */
export function isDirectStreamMedia(media?: Media | null): boolean {
  if (!media) return false
  return isRemoteEmbyID(media.id)
}

/**
 * STRM / 云盘直链：默认仍走直连；浏览器解不了时再回退 HLS 转码。
 */
export function isStrmMedia(media?: Media | null): boolean {
  if (!media) return false
  const container = (media.container ?? '').toLowerCase()
  return container.includes('strm') || String(media.strm_url ?? '').trim() !== ''
}

export function pickPlayerMode(media: Media): PlayerMode {
  return needsTranscodeForBrowser(media) ? 'hls' : 'direct'
}

export function needsTranscodeForBrowser(media: Media): boolean {
  // Emby 远程挂载无法本地转码。STRM 先直连，失败后再由播放器切 HLS。
  if (isDirectStreamMedia(media) || isStrmMedia(media)) return false

  const container = (media.container ?? '').toLowerCase()
  const videoCodec = (media.video_codec ?? '').toLowerCase()
  const audioCodec = (media.audio_codec ?? '').toLowerCase()
  const containerOK = directContainers.some((item) => container.includes(item))
  const videoOK = !videoCodec || directVideoCodecs.some((item) => videoCodec.includes(item))
  const audioOK = !audioCodec || directAudioCodecs.some((item) => audioCodec.includes(item))
  return !(containerOK && videoOK && audioOK)
}

